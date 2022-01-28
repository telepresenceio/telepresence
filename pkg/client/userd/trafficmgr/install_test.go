package trafficmgr

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	goRuntime "runtime"
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dtest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func TestAddAgentToWorkload(t *testing.T) {
	// Part 1: Build the testcases /////////////////////////////////////////
	type testcase struct {
		InputVersion  string
		InputPortName string
		InputWorkload k8sapi.Workload
		InputService  *core.Service

		OutputWorkload k8sapi.Workload
		OutputService  *core.Service
	}
	testcases := map[string]testcase{}

	dirinfos, err := os.ReadDir("testdata/addAgentToWorkload")
	if err != nil {
		t.Fatal(err)
	}
	i := 0
	for _, di := range dirinfos {
		fileinfos, err := os.ReadDir(filepath.Join("testdata/addAgentToWorkload", di.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, fi := range fileinfos {
			if !strings.HasSuffix(fi.Name(), ".input.yaml") {
				continue
			}
			tcName := di.Name() + "/" + strings.TrimSuffix(fi.Name(), ".input.yaml")

			var tc testcase
			var err error

			tc.InputVersion = di.Name()
			if tc.InputVersion == "cur" {
				// Must alway be higher than any actually released version, so pack
				// a bunch of 9's in there.
				tc.InputVersion = fmt.Sprintf("v2.999.999-gotest.%d.%d", os.Getpid(), i)
				i++
			}

			tc.InputWorkload, tc.InputService, tc.InputPortName, err = loadFile(tcName+".input.yaml", tc.InputVersion)
			if err != nil {
				t.Fatal(err)
			}

			tc.OutputWorkload, tc.OutputService, _, err = loadFile(tcName+".output.yaml", tc.InputVersion)
			if err != nil {
				t.Fatal(err)
			}

			testcases[tcName] = tc
		}
	}

	// Part 2: Run the testcases in "install" mode /////////////////////////
	ctx := dlog.NewTestContext(t, true)
	env, err := client.LoadEnv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ctx = client.WithEnv(ctx, env)
	cfg, err := client.LoadConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ctx = client.WithConfig(ctx, cfg)

	// We use the MachineLock here since we have to set and reset the version.Version
	dtest.WithMachineLock(ctx, func(ctx context.Context) {
		sv := version.Version
		defer func() { version.Version = sv }()

		testCfg := *cfg
		testCfg.Images.Registry = "localhost:5000"
		ctx = client.WithConfig(ctx, &testCfg)

		for tcName, tc := range testcases {
			tcName := tcName // "{version-dir}/{yaml-base-name}"
			tc := tc
			if !strings.HasPrefix(tcName, "cur/") {
				// Don't check install for historical snapshots.
				continue
			}

			t.Run(tcName+"/install", func(t *testing.T) {
				version.Version = tc.InputVersion

				expectedWrk := deepCopyObject(tc.OutputWorkload)
				sanitizeWorkload(expectedWrk)

				expectedSvc := tc.OutputService.DeepCopy()
				sanitizeService(expectedSvc)

				apiPort := uint16(0)
				if tcName == "cur/deployment-tpapi" {
					apiPort = 9901
				}
				svc := tc.InputService.DeepCopy()
				obj := deepCopyObject(tc.InputWorkload)
				svcname := managerImageName(ctx)
				servicePort, container, containerPortIndex, err := exploreSvc(ctx, svc, tc.InputPortName, svcname, obj)
				if !assert.NoError(t, err) {
					return
				}

				actualWrk, actualSvc, _, actualErr := addAgentToWorkload(ctx,
					servicePort,
					container,
					containerPortIndex,
					svcname, // ignore extensions
					env.ManagerNamespace,
					apiPort,
					obj,
					svc,
				)
				if !assert.NoError(t, actualErr) {
					return
				}

				sanitizeWorkload(actualWrk)
				assert.Equal(t, expectedWrk, actualWrk)

				if actualSvc != nil {
					actualSvcImpl, _ := k8sapi.ServiceImpl(actualSvc)
					sanitizeService(actualSvcImpl)
					assert.Equal(t, expectedSvc, actualSvcImpl)
				}

				if t.Failed() && os.Getenv("DEV_TELEPRESENCE_GENERATE_GOLD") != "" {
					workloadKind := actualWrk.GetObjectKind().GroupVersionKind().Kind

					goldBytes, err := yaml.Marshal(map[string]interface{}{
						strings.ToLower(workloadKind): actualWrk,
						"service":                     actualSvc,
					})
					if !assert.NoError(t, err) {
						return
					}
					goldBytes = bytes.ReplaceAll(goldBytes,
						[]byte(strings.TrimPrefix(version.Version, "v")),
						[]byte("{{.Version}}"))

					err = os.WriteFile(
						filepath.Join("testdata/addAgentToWorkload", tcName+".output.yaml"),
						goldBytes,
						0644)
					assert.NoError(t, err)
				}
			})
		}

		// Part 3: Run the testcases in "uninstall" mode ///////////////////////
		for tcName, tc := range testcases {
			tc := tc
			t.Run(tcName+"/uninstall", func(t *testing.T) {
				version.Version = tc.InputVersion

				expectedWrk := deepCopyObject(tc.InputWorkload)
				sanitizeWorkload(expectedWrk)

				expectedSvc := tc.InputService.DeepCopy()
				sanitizeService(expectedSvc)

				actualWrk := deepCopyObject(tc.OutputWorkload)
				_, actualErr := undoObjectMods(ctx, actualWrk)
				if !assert.NoError(t, actualErr) {
					return
				}
				sanitizeWorkload(actualWrk)

				actualSvc := tc.OutputService.DeepCopy()
				actualErr = undoServiceMods(ctx, k8sapi.Service(actualSvc))
				if !assert.NoError(t, actualErr) {
					return
				}
				sanitizeService(actualSvc)

				assert.Equal(t, expectedWrk, actualWrk)
				assert.Equal(t, expectedSvc, actualSvc)
			})
		}
	})
}

func sanitizeWorkload(obj k8sapi.Workload) {
	mObj := obj.(metav1.ObjectMetaAccessor).GetObjectMeta()
	mObj.SetResourceVersion("")
	mObj.SetGeneration(int64(0))
	mObj.SetCreationTimestamp(metav1.Time{})
	podTemplate := obj.GetPodTemplate()
	for i, c := range podTemplate.Spec.Containers {
		c.TerminationMessagePath = ""
		c.TerminationMessagePolicy = ""
		c.ImagePullPolicy = ""
		if goRuntime.GOOS == "windows" && c.Name == "traffic-agent" {
			for j, v := range c.VolumeMounts {
				v.MountPath = filepath.Clean(v.MountPath)
				c.VolumeMounts[j] = v
			}
		}
		podTemplate.Spec.Containers[i] = c
	}
}

func sanitizeService(svc *core.Service) {
	svc.ObjectMeta.ResourceVersion = ""
	svc.ObjectMeta.Generation = 0
	svc.ObjectMeta.CreationTimestamp = metav1.Time{}
}

func deepCopyObject(obj k8sapi.Workload) k8sapi.Workload {
	wl, err := k8sapi.WrapWorkload(obj.DeepCopyObject())
	if err != nil {
		panic(err)
	}
	return wl
}

// loadFile is a helper function that reads test data files and converts them
// to a format that can be used in the tests.
func loadFile(filename, inputVersion string) (workload k8sapi.Workload, service *core.Service, portname string, err error) {
	tmpl, err := template.ParseFiles(filepath.Join("testdata/addAgentToWorkload", filename))
	if err != nil {
		return nil, nil, "", fmt.Errorf("read template: %s: %w", filename, err)
	}

	var buff bytes.Buffer
	err = tmpl.Execute(&buff, map[string]interface{}{
		"Version": strings.TrimPrefix(inputVersion, "v"),
	})
	if err != nil {
		return nil, nil, "", fmt.Errorf("execute template: %s: %w", filename, err)
	}

	var dat struct {
		Deployment  *apps.Deployment  `json:"deployment"`
		ReplicaSet  *apps.ReplicaSet  `json:"replicaset"`
		StatefulSet *apps.StatefulSet `json:"statefulset"`

		Service       *core.Service `json:"service"`
		InterceptPort string        `json:"interceptPort"`
	}
	if err := yaml.Unmarshal(buff.Bytes(), &dat); err != nil {
		return nil, nil, "", fmt.Errorf("parse yaml: %s: %w", filename, err)
	}

	cnt := 0
	if dat.Deployment != nil {
		cnt++
		workload = k8sapi.Deployment(dat.Deployment)
	}
	if dat.ReplicaSet != nil {
		cnt++
		workload = k8sapi.ReplicaSet(dat.ReplicaSet)
	}
	if dat.StatefulSet != nil {
		cnt++
		workload = k8sapi.StatefulSet(dat.StatefulSet)
	}
	if cnt != 1 {
		return nil, nil, "", fmt.Errorf("yaml must contain exactly one of 'deployment', 'replicaset', or 'statefulset'; got %d of them", cnt)
	}

	return workload, dat.Service, dat.InterceptPort, nil
}

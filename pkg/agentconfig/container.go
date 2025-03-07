package agentconfig

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-json-experiment/json"
	core "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
)

type ContainerBuilder struct {
	MountPolicies MountPolicies
	Pod           *core.Pod
	Config        *Sidecar
}

// AgentContainer will return a configured traffic-agent.
func (a *ContainerBuilder) AgentContainer(ctx context.Context) (*core.Container, map[string]string) {
	ports := make([]core.ContainerPort, 0, 5)
	confCns := a.configuredContainers(ctx)

	a.eachConfiguredContainer(confCns, func(app *core.Container, cc *Container) {
		if cc.Replace == ReplacePolicyContainer {
			// Simply inherit the ports of the replaced container
			ports = append(ports, app.Ports...)
		} else if cc.Replace == ReplacePolicyIntercept {
			for _, ic := range PortUniqueIntercepts(cc) {
				ports = append(ports, core.ContainerPort{
					Name:          ic.ContainerPortName,
					ContainerPort: int32(ic.AgentPort),
					Protocol:      ic.Protocol,
				})
			}
		}
	})

	evs := make([]core.EnvVar, 0, len(a.Config.Containers)*5)
	efs := make([]core.EnvFromSource, 0, len(a.Config.Containers)*3)
	a.eachConfiguredContainer(confCns, func(app *core.Container, cc *Container) {
		evs = appendAppContainerEnv(app, cc, evs)
		efs = appendAppContainerEnvFrom(app, cc, efs)
	})
	if a.Config.APIPort > 0 {
		evs = append(evs, core.EnvVar{
			Name:  EnvAPIPort,
			Value: strconv.Itoa(int(a.Config.APIPort)),
		})
	}
	evs = append(evs,
		core.EnvVar{
			Name: "AGENT_CONFIG",
			ValueFrom: &core.EnvVarSource{
				FieldRef: &core.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  fmt.Sprintf("metadata.annotations['%s']", ConfigAnnotation),
				},
			},
		},
		core.EnvVar{
			Name: EnvPrefixAgent + "POD_IP",
			ValueFrom: &core.EnvVarSource{
				FieldRef: &core.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "status.podIP",
				},
			},
		},
		core.EnvVar{
			Name: EnvPrefixAgent + "POD_UID",
			ValueFrom: &core.EnvVarSource{
				FieldRef: &core.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "metadata.uid",
				},
			},
		},
		core.EnvVar{
			Name: EnvPrefixAgent + "NAME",
			ValueFrom: &core.EnvVarSource{
				FieldRef: &core.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "metadata.name",
				},
			},
		})

	mounts := make([]core.VolumeMount, 0, len(a.Config.Containers)*3)
	a.eachConfiguredContainer(confCns, func(app *core.Container, cc *Container) {
		mounts = a.appendVolumeMounts(app, cc, mounts)
	})
	mounts = append(mounts,
		core.VolumeMount{
			Name:      ExportsVolumeName,
			MountPath: ExportsMountPoint,
		},
		core.VolumeMount{
			Name:      TempVolumeName,
			MountPath: TempMountPoint,
		},
	)

	if len(efs) == 0 {
		efs = nil
	}

	annotations := make(map[string]string)
	a.eachConfiguredContainer(confCns, func(app *core.Container, cc *Container) {
		if cc.Replace == ReplacePolicyContainer {
			cnJson, err := json.Marshal(app)
			if err != nil {
				dlog.Errorf(ctx, "unable to marshal container %s.%s/%s to json: %v", a.Config.WorkloadName, a.Config.Namespace, app.Name, err)
			}
			annotations[ReplaceAnnotationKey(cc.Name)] = string(cnJson)
		}
	})

	cfg, _ := MarshalTight(a.Config)
	annotations[ConfigAnnotation] = cfg

	if len(ports) == 0 {
		ports = nil
	}
	ac := &core.Container{
		Name:         ContainerName,
		Image:        a.Config.AgentImage,
		Args:         []string{"agent"},
		Ports:        ports,
		Env:          evs,
		EnvFrom:      efs,
		VolumeMounts: mounts,
		ReadinessProbe: &core.Probe{
			ProbeHandler: core.ProbeHandler{
				Exec: &core.ExecAction{
					Command: []string{"/bin/stat", "/tmp/agent/ready"},
				},
			},
		},
		ImagePullPolicy: core.PullPolicy(a.Config.PullPolicy),
	}
	if r := a.Config.Resources; r != nil {
		ac.Resources = *r
	}

	appSc := a.Config.SecurityContext
	if appSc == nil {
		var err error
		// Assign the security context of the first container to the traffic agent.
		appSc, err = a.firstAppSecurityContext()
		if err != nil {
			return nil, nil
		}
	}
	ac.SecurityContext = appSc

	return ac, annotations
}

func ReplaceAnnotationKey(cn string) string {
	return ReplacedContainerAnnotationPrefix + cn
}

// Find the security context of the first container (with both intercepts and a set security context) and ensure
// that any env interpolations in it are prefixed with the env-prefix of the corresponding config container.
func (a *ContainerBuilder) firstAppSecurityContext() (*core.SecurityContext, error) {
	cns := a.Pod.Spec.Containers
	for _, cc := range a.Config.Containers {
		if len(cc.Intercepts) > 0 {
			for i := range cns {
				app := &cns[i]
				if app.Name != cc.Name {
					continue
				}
				if app.SecurityContext == nil {
					break
				}
				js, err := json.Marshal(app.SecurityContext)
				if err != nil {
					return nil, err
				}
				sc := core.SecurityContext{}
				err = json.Unmarshal([]byte(prefixInterpolated(string(js), EnvPrefixApp+cc.EnvPrefix)), &sc)
				if err != nil {
					return nil, err
				}
				return &sc, nil
			}
		}
	}
	return nil, nil
}

// configuredContainers will find each container in the given config and match it against a container
// in the pod using its name. The returned slice is guaranteed to use the same index as the Sidecar.Containers slice.
func (a *ContainerBuilder) configuredContainers(ctx context.Context) []*core.Container {
	cns := a.Pod.Spec.Containers
	result := make([]*core.Container, len(a.Config.Containers))
	for ci, cc := range a.Config.Containers {
		for i := range cns {
			app := &cns[i]
			if app.Name == ContainerName {
				// The pod might hold JSON of replaced containers from an earlier patch
				annName := ReplacedContainerAnnotationPrefix + cc.Name
				if appJson, ok := a.Pod.ObjectMeta.Annotations[annName]; ok {
					var cn core.Container
					err := json.Unmarshal([]byte(appJson), &cn)
					if err != nil {
						dlog.Errorf(ctx, "failed to unmarshal container annotation %s: %v", annName, err)
					}
					result[ci] = &cn
					break
				}
			} else if app.Name == cc.Name {
				result[ci] = app
				break
			}
		}
	}
	return result
}

func (a *ContainerBuilder) eachConfiguredContainer(configureContainers []*core.Container, f func(*core.Container, *Container)) {
	for i, cn := range configureContainers {
		if cn != nil {
			f(cn, a.Config.Containers[i])
		}
	}
}

// prefixInterpolated will prefix all environment variable names that are referenced using $(NAME) expressions
// in the given string with the given prefix and return the result. Escaped expressions in the form $$(NAME),
// unbalanced, or otherwise invalid expressions are not prefixed.
func prefixInterpolated(str, pfx string) string {
	const (
		stNormal = iota
		stDollarSeen
		stDollarParenSeen
	)
	st := stNormal
	var bd, ev strings.Builder
	for _, c := range str {
		switch c {
		case '$':
			switch st {
			case stDollarParenSeen:
				// '$' is not a legal character in an environment interpolation expression so
				// terminate that expression without prefixing it.
				bd.WriteString(ev.String())
				ev.Reset()
				st = stDollarSeen
			case stDollarSeen:
				st = stNormal
			default:
				st = stDollarSeen
			}
			bd.WriteByte('$')
		case '(':
			switch st {
			case stDollarParenSeen:
				// '(' is not a legal character in an environment interpolation expression so
				// terminate that expression without prefixing it.
				bd.WriteString(ev.String())
				ev.Reset()
				st = stNormal
			case stDollarSeen:
				st = stDollarParenSeen
			default:
				st = stNormal
			}
			bd.WriteByte('(')
		case ')':
			if st == stDollarParenSeen && ev.Len() > 0 {
				bd.WriteString(pfx)
				bd.WriteString(ev.String())
				ev.Reset()
			}
			st = stNormal
			bd.WriteByte(')')
		default:
			switch st {
			case stDollarParenSeen:
				ev.WriteRune(c)
			default:
				bd.WriteRune(c)
				st = stNormal
			}
		}
	}
	if ev.Len() > 0 {
		// Unbalanced interpolation. Just leave it as is.
		bd.WriteString(ev.String())
	}
	return bd.String()
}

var envRxReplace = regexp.MustCompile(`\$\(([^)]+)\)`)

func appendAppContainerEnv(app *core.Container, cc *Container, es []core.EnvVar) []core.EnvVar {
	pfx := EnvPrefixApp + cc.EnvPrefix
	pfxReplace := "$(" + pfx + "$1)"
	for _, e := range app.Env {
		e.Name = pfx + e.Name
		e.Value = envRxReplace.ReplaceAllString(e.Value, pfxReplace)
		es = append(es, e)
	}
	return es
}

func appendAppContainerEnvFrom(app *core.Container, cc *Container, es []core.EnvFromSource) []core.EnvFromSource {
	for _, e := range app.EnvFrom {
		e.Prefix = EnvPrefixApp + cc.EnvPrefix + e.Prefix
		es = append(es, e)
	}
	return es
}

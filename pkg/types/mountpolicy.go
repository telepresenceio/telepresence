package types

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"

	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
)

type MountPolicy int

type MountPolicies map[string]MountPolicy

const (
	// MountPolicyRemote means that the client can (or in case of a docker-run, will) mount the
	// volume using a remote file system. Unless constrained by other mechanisms, the mount will
	// be read-write.
	MountPolicyRemote MountPolicy = iota
	// MountPolicyRemoteReadOnly is like MountPolicyRemote but will enforce a read-only mount.
	MountPolicyRemoteReadOnly

	// MountPolicyLocal means that the mount will be confined to the workstation. This is typically
	// the case for /tmp.
	MountPolicyLocal

	// MountPolicyIgnore means that the mount will be completely ignored by Telepresence.
	MountPolicyIgnore
)

var mountPolicyNames = []string{"Remote", "RemoteReadonly", "Local", "Ignore"} //nolint:gochecknoglobals // constant

func (mp MountPolicy) String() string {
	if mp >= 0 && int(mp) < len(mountPolicyNames) {
		return mountPolicyNames[mp]
	}
	return "Unknown"
}

func (mp MountPolicy) MarshalJSONTo(out *jsontext.Encoder) error {
	return json.MarshalEncode(out, mp.String())
}

func (mp *MountPolicy) UnmarshalJSONFrom(in *jsontext.Decoder) error {
	var s string
	err := json.UnmarshalDecode(in, &s)
	if err == nil {
		if ix := slices.Index(mountPolicyNames, s); ix >= 0 {
			*mp = MountPolicy(ix)
		} else {
			err = fmt.Errorf("invalid mount policy: %q", s)
		}
	}
	return err
}

func (iv MountPolicies) AddAnnotations(ctx context.Context, annotations map[string]string) (MountPolicies, error) {
	ignores, err := iv.getIgnoreAnnotations(ctx, annotations)
	if err != nil {
		return nil, err
	}
	policies, err := iv.getPolicyAnnotations(annotations)
	if err != nil {
		return nil, err
	}
	if len(ignores) == 0 && len(policies) == 0 {
		return iv, nil
	}
	mps := maps.Clone(iv)
	for key, policy := range policies {
		mps[key] = policy
	}
	for _, key := range ignores {
		mps[key] = MountPolicyIgnore
	}
	return mps, nil
}

func MountPoliciesFromRPC(mr map[string]int32) MountPolicies {
	if mr == nil {
		return nil
	}
	mps := make(MountPolicies, len(mr))
	for k, v := range mr {
		mps[k] = MountPolicy(v)
	}
	return mps
}

func (iv MountPolicies) ToRPC() map[string]int32 {
	if len(iv) == 0 {
		return nil
	}
	mr := make(map[string]int32, len(iv))
	for key, policy := range iv {
		mr[key] = int32(policy)
	}
	return mr
}

func (iv MountPolicies) getPolicyAnnotations(anns map[string]string) (mps MountPolicies, err error) {
	vma, ok := anns[annotation.VolumeMountPolicies]
	if !ok {
		return nil, nil
	}
	vma = strings.TrimSpace(vma)
	if len(vma) == 0 {
		return nil, nil
	}

	// Unmarshalling into the clone overwrites existing entries in the clone. This is intentional. The
	// annotation has higher priority.
	err = json.Unmarshal([]byte(vma), &mps)
	return mps, err
}

func (iv MountPolicies) getIgnoreAnnotations(ctx context.Context, anns map[string]string) (ignores []string, err error) {
	vma := annotation.GetAnnotation(ctx, anns, annotation.InjectIgnoreVolumeMounts, annotation.LegacyInjectIgnoreVolumeMounts)
	vma = strings.TrimSpace(vma)
	if len(vma) == 0 {
		return nil, nil
	}

	// We accept two formats.
	// 1. A JSON []string (all entries considered to be MountPolicyIgnore)
	// 2. A comma separated []string (all entries considered to be MountPolicyIgnore)
	switch vma[0] {
	case '[':
		err = json.Unmarshal([]byte(vma), &ignores)
	default:
		ignores = strings.Split(vma, ",")
		for i, vm := range ignores {
			ignores[i] = strings.TrimSpace(vm)
		}
	}
	return ignores, err
}

func (iv MountPolicies) Get(volumeName, mountPath string) MountPolicy {
	for key, policy := range iv {
		if key == volumeName || strings.HasPrefix(mountPath, key) {
			return policy
		}
	}
	return MountPolicyRemote
}

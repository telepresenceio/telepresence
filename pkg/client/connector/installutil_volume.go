package connector

import (
	corev1 "k8s.io/api/core/v1"
)

func AgentVolume() corev1.Volume {
	return corev1.Volume{
		Name: agentAnnotationVolumeName,
		VolumeSource: corev1.VolumeSource{
			DownwardAPI: &corev1.DownwardAPIVolumeSource{
				Items: []corev1.DownwardAPIVolumeFile{
					{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.annotations",
						},
						Path: "annotations",
					},
				},
			},
		},
	}
}

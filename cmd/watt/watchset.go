package main

/*
type WatchSet struct {
	KubernetesWatches []KubernetesWatch
	ConsulWatches     []ConsulWatch
}

type KubernetesWatch struct {
	Kind          string
	Namespace     string
	FieldSelector string
	LabelSelector string
}
*/

type ConsulWatch struct {
	ConsulAddress string
	Datacenter    string
	ServiceName   string
}

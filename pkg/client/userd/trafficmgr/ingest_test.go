package trafficmgr

import (
	"testing"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestSession_findIngest(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(*session)
		workloadName  string
		containerName string
		wantErr       bool
		wantErrCode   codes.Code
		wantErrMsg    string
		validate      func(*testing.T, *ingest)
	}{
		{
			name:          "finds ingest with workload and container specified",
			workloadName:  "my-workload",
			containerName: "my-container",
			setup: func(s *session) {
				ig := &ingest{
					ingestKey: ingestKey{
						workload:  "my-workload",
						container: "my-container",
					},
					AgentInfo: &manager.AgentInfo{
						Name: "my-workload",
						Containers: map[string]*manager.AgentInfo_ContainerInfo{
							"my-container": {},
						},
					},
				}
				s.currentIngests.Store(ig.ingestKey, ig)
			},
			wantErr: false,
			validate: func(t *testing.T, ig *ingest) {
				assert.Equal(t, "my-workload", ig.workload)
				assert.Equal(t, "my-container", ig.container)
			},
		},
		{
			name:          "finds ingest with only workload specified (single container)",
			workloadName:  "my-workload",
			containerName: "",
			setup: func(s *session) {
				ig := &ingest{
					ingestKey: ingestKey{
						workload:  "my-workload",
						container: "my-container",
					},
					AgentInfo: &manager.AgentInfo{
						Name: "my-workload",
						Containers: map[string]*manager.AgentInfo_ContainerInfo{
							"my-container": {},
						},
					},
				}
				s.currentIngests.Store(ig.ingestKey, ig)
			},
			wantErr: false,
			validate: func(t *testing.T, ig *ingest) {
				assert.Equal(t, "my-workload", ig.workload)
				assert.Equal(t, "my-container", ig.container)
			},
		},
		{
			name:          "errors when workload has multiple containers and no container specified",
			workloadName:  "my-workload",
			containerName: "",
			setup: func(s *session) {
				ig1 := &ingest{
					ingestKey: ingestKey{
						workload:  "my-workload",
						container: "container-1",
					},
					AgentInfo: &manager.AgentInfo{
						Name: "my-workload",
						Containers: map[string]*manager.AgentInfo_ContainerInfo{
							"container-1": {},
						},
					},
				}
				ig2 := &ingest{
					ingestKey: ingestKey{
						workload:  "my-workload",
						container: "container-2",
					},
					AgentInfo: &manager.AgentInfo{
						Name: "my-workload",
						Containers: map[string]*manager.AgentInfo_ContainerInfo{
							"container-2": {},
						},
					},
				}
				s.currentIngests.Store(ig1.ingestKey, ig1)
				s.currentIngests.Store(ig2.ingestKey, ig2)
			},
			wantErr:     true,
			wantErrCode: codes.NotFound,
			wantErrMsg:  "workload my-workload has multiple ingests",
		},
		{
			name:          "errors when ingest doesn't exist",
			workloadName:  "nonexistent",
			containerName: "container",
			setup:         func(s *session) {},
			wantErr:       true,
			wantErrCode:   codes.NotFound,
			wantErrMsg:    "doesn't exist",
		},
		{
			name:          "errors when no ingest found for workload",
			workloadName:  "nonexistent",
			containerName: "",
			setup:         func(s *session) {},
			wantErr:       true,
			wantErrCode:   codes.NotFound,
			wantErrMsg:    "no ingest found for workload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &session{
				currentIngests: xsync.NewMap[ingestKey, *ingest](),
			}

			if tt.setup != nil {
				tt.setup(s)
			}

			ig, err := s.findIngest(tt.workloadName, tt.containerName)

			if tt.wantErr {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok, "error should be a gRPC status error")
				assert.Equal(t, tt.wantErrCode, st.Code())
				assert.Contains(t, st.Message(), tt.wantErrMsg)
				assert.Nil(t, ig)
			} else {
				require.NoError(t, err)
				require.NotNil(t, ig)
				if tt.validate != nil {
					tt.validate(t, ig)
				}
			}
		})
	}
}

/*
   Copyright 2020 Docker Compose CLI authors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package progress

import (
	"context"
	"fmt"
	"io"

	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

type plainWriter struct {
	out io.Writer
	err io.Writer
}

func (p plainWriter) IsNoOp() bool {
	return false
}

func (p plainWriter) Start(context.Context, string) {
}

func (p plainWriter) Stop() {
}

func (p plainWriter) Write(events ...*Event) {
	for _, e := range events {
		w := p.out
		if e.Status == EventStatusError || e.Status == EventStatusWarning {
			w = p.err
		}
		if e.plainAlways || e.Status == EventStatusError || e.Status == EventStatusWarning || e.Status == EventStatusInfo || e.Text != "" {
			if e.Text == "" {
				ioutil.Println(w, e.StatusText)
			} else {
				ioutil.Println(w, e.Text, e.StatusText)
			}
		}
		p.Write(e.children...)
	}
}

func (p plainWriter) TailMsgf(msg string, args ...any) {
	_, _ = fmt.Fprintln(p.out, fmt.Sprintf(msg, args...))
}

func (p plainWriter) TriggerRefresh() {
}

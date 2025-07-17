/*
   Copyright 2024 Docker Compose CLI authors

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

	"github.com/go-json-experiment/json"
)

type jsonWriter struct {
	out io.Writer
}

func (p *jsonWriter) Start(context.Context, string) {
}

func (p *jsonWriter) IsNoOp() bool {
	return false
}

func (p *jsonWriter) write(e *Event) {
	marshal, err := json.Marshal(e)
	if err == nil {
		_, _ = fmt.Fprintln(p.out, string(marshal))
	}
}

func (p *jsonWriter) Write(events ...*Event) {
	for _, e := range events {
		p.write(e)
	}
}

type tailMsg struct {
	Message string `json:"message"`
}

func (p *jsonWriter) TailMsgf(msg string, args ...any) {
	marshal, err := json.Marshal(&tailMsg{Message: fmt.Sprintf(msg, args...)})
	if err == nil {
		_, _ = fmt.Fprintln(p.out, string(marshal))
	}
}

func (p *jsonWriter) Stop() {
}

func (p *jsonWriter) TriggerRefresh() {
}

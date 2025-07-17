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

import "context"

type quiet struct{}

func (q quiet) Start(context.Context, string) {
}

func (q quiet) IsNoOp() bool {
	return false
}

func (q quiet) Stop() {
}

func (q quiet) Write(...*Event) {
}

func (q quiet) TailMsgf(_ string, _ ...any) {
}

func (q quiet) TriggerRefresh() {
}

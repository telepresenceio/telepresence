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
	"github.com/morikuni/aec"
)

type noColor struct{}

func (a noColor) With(_ ...aec.ANSI) aec.ANSI {
	return a
}

func (noColor) Apply(s string) string {
	return s
}

func (noColor) String() string {
	return ""
}

var (
	doneColor    = aec.BlueF                  //nolint:gochecknoglobals // constant names
	timerColor   = aec.BlueF                  //nolint:gochecknoglobals // constant names
	countColor   = aec.YellowF                //nolint:gochecknoglobals // constant names
	infoColor    = aec.LightBlueF             //nolint:gochecknoglobals // constant names
	warningColor = aec.YellowF.With(aec.Bold) //nolint:gochecknoglobals // constant names
	successColor = aec.GreenF                 //nolint:gochecknoglobals // constant names
	errorColor   = aec.RedF.With(aec.Bold)    //nolint:gochecknoglobals // constant names
)

package route

import (
	"encoding/json"
	"reflect"
	"testing"
)

var tables = []struct {
	in  string
	out *Table
	err string
}{
	{"", nil, "unexpected end of JSON input"},
	{`
{
  "name": "table",
  "routes": [
    {"name": "foo", "ip": "bar", "proto": "baz"}
  ]
}
`,
		&Table{
			Name: "table",
			Routes: []Route{
				{Name: "foo", Ip: "bar", Proto: "baz"},
			},
		}, ""},
}

func TestDecode(t *testing.T) {
	for _, tt := range tables {
		table := Table{}
		err := json.Unmarshal([]byte(tt.in), &table)
		if err != nil {
			if err.Error() != tt.err {
				t.Errorf("got %v, expected %v", err, tt.err)
			}
		} else {
			if !reflect.DeepEqual(&table, tt.out) {
				t.Errorf("got %v, expected %v", table, tt.out)
			}
		}
	}
}

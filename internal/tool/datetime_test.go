package tool

import (
	"context"
	"encoding/json"
	"testing"
)

func TestDatetime(t *testing.T) {
	cases := []struct {
		name    string
		args    string
		wantErr bool
	}{
		{"指定时区", `{"tz":"America/Los_Angeles"}`, false},
		{"不填时区", `{}`, false},
		{"不存在的时区", `{"tz":"Mars/Base"}`, true},
		{"类型错误", `{"tz":123}`, true}, // nexus-agent 这里会静默用默认时区
	}
	tool := Datetime()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := tool.Execute(context.Background(), "id", json.RawMessage(c.args), nil)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v，wantErr = %v", err, c.wantErr)
			}
			if !c.wantErr && res.Content == "" {
				t.Error("成功时 Content 不应为空")
			}
		})
	}
}

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestDescribeWSDisconnect(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"eof 帧头", fmt.Errorf("failed to get reader: failed to read frame header: %w", io.EOF), "未正常关闭连接"},
		{"本端关闭", fmt.Errorf("failed to get reader: %w", net.ErrClosed), "被中心关闭"},
		{"正常关闭", websocket.CloseError{Code: websocket.StatusNormalClosure}, "正常关闭"},
		{"重启", websocket.CloseError{Code: websocket.StatusGoingAway}, "重启或退出"},
		{"其他关闭码", websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: "x"}, "1008"},
		{"取消", context.Canceled, "已取消"},
		{"超时", context.DeadlineExceeded, "超时"},
		{"重置", errors.New("read tcp 1.1.1.1:8092: connection reset by peer"), "重置"},
		{"未知", errors.New("boom"), "boom"},
	}
	for _, tc := range cases {
		got := describeWSDisconnect(tc.err)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q 不含 %q", tc.name, got, tc.want)
		}
		if strings.Contains(got, "failed to get reader") || strings.Contains(got, "frame header") {
			t.Errorf("%s: 仍带着库的原始报错: %q", tc.name, got)
		}
	}
	if got := describeWSDisconnect(nil); got == "" {
		t.Error("nil 应有兜底文案")
	}
}

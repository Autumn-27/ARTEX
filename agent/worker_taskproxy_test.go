package agent

import "testing"

// proxyForTask:任务 resolver 命中用任务实例,未命中/未设置回落全局 SetProxy。
func TestWorkerProxyForTask(t *testing.T) {
	w := &Worker{proxyAddr: "http://global:8788", proxyCACert: "/data/traffic/_ca/cert.pem"}

	// 未装 resolver → 全局。
	a, c := w.proxyForTask(7)
	if a != w.proxyAddr || c != w.proxyCACert {
		t.Errorf("无 resolver 应用全局代理, got (%q, %q)", a, c)
	}

	// resolver 返回空 → 回落全局。
	w.SetTaskProxyResolver(func(taskID int64) (string, string) { return "", "" })
	a, c = w.proxyForTask(7)
	if a != w.proxyAddr || c != w.proxyCACert {
		t.Errorf("resolver 空结果应回落全局, got (%q, %q)", a, c)
	}

	// resolver 命中 → 任务实例地址+CA,且按 taskID 传参。
	w.SetTaskProxyResolver(func(taskID int64) (string, string) {
		if taskID != 7 {
			t.Errorf("resolver 应收到任务 id 7, got %d", taskID)
		}
		return "http://task:21100", "/data/traffic-tasks/7/_ca/cert.pem"
	})
	a, c = w.proxyForTask(7)
	if a != "http://task:21100" || c != "/data/traffic-tasks/7/_ca/cert.pem" {
		t.Errorf("resolver 命中应用任务实例, got (%q, %q)", a, c)
	}
}

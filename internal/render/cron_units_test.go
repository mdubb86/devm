package render

import (
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
)

func TestRenderCronJob_Service(t *testing.T) {
	svc, _, _, err := RenderCronJob("sync-gdevm-docs", "0 * * * *", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := string(svc)
	for _, want := range []string{
		"[Unit]",
		"[Service]",
		"Description=",
		"Type=oneshot",
		"User=devm",
		"EnvironmentFile=-/etc/environment",
		"After=devm-ready.target",
		"ExecStart=/opt/devm/cron-wrappers/sync-gdevm-docs.sh",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("service missing %q:\n%s", want, s)
		}
	}
	for _, bad := range []string{"Restart=", "[Install]"} {
		if strings.Contains(s, bad) {
			t.Errorf("service must not contain %q:\n%s", bad, s)
		}
	}
}

func TestRenderCronJob_ServiceRequiresReadyTarget(t *testing.T) {
	svc, _, _, err := RenderCronJob("fn", "0 * * * *", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(svc), "Requires=devm-ready.target") {
		t.Errorf("service must require devm-ready.target like function services:\n%s", svc)
	}
}

func TestRenderCronJob_Timer(t *testing.T) {
	_, timer, _, err := RenderCronJob("sync-gdevm-docs", "0 * * * *", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := string(timer)
	for _, want := range []string{
		"[Timer]",
		"OnCalendar=*-*-* *:00:00",
		"Persistent=false",
		"Unit=devm-cron-sync-gdevm-docs.service",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("timer missing %q:\n%s", want, s)
		}
	}
	for _, bad := range []string{"[Install]", "WantedBy"} {
		if strings.Contains(s, bad) {
			t.Errorf("timer must not contain %q:\n%s", bad, s)
		}
	}
}

func TestRenderCronJob_WrapperMatchesServiceWrapper(t *testing.T) {
	env := map[string]string{"FOO": "bar", "BAZ": "qux"}
	path := []string{"/opt/tool/bin"}
	_, _, got, err := RenderCronJob("job", "0 * * * *", env, path)
	if err != nil {
		t.Fatal(err)
	}
	svcEnv := map[string]schema.EnvValue{}
	for k, v := range env {
		svcEnv[k] = schema.EnvValue{Literal: v}
	}
	_, _, want, err := RenderService(schema.Service{ExecFunc: "job", Env: svcEnv}, "job", nil, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 || string(got) != string(want) {
		t.Errorf("wrapper mismatch:\ncron:\n%s\nservice:\n%s", got, want)
	}
}

func TestRenderCronJob_BadWrapper(t *testing.T) {
	if _, _, _, err := RenderCronJob("", "0 * * * *", nil, nil); err == nil {
		t.Fatal("expected error for empty function name")
	}
}

func TestCronNames(t *testing.T) {
	const fn = "sync-gdevm-docs"
	if got := CronServiceUnit(fn); got != "devm-cron-sync-gdevm-docs.service" {
		t.Errorf("service unit = %q", got)
	}
	if got := CronTimerUnit(fn); got != "devm-cron-sync-gdevm-docs.timer" {
		t.Errorf("timer unit = %q", got)
	}
	if got := CronWrapperPath(fn); got != "/opt/devm/cron-wrappers/sync-gdevm-docs.sh" {
		t.Errorf("wrapper path = %q", got)
	}
}

func TestRenderCronJob_BadSpec(t *testing.T) {
	_, _, _, err := RenderCronJob("sync-gdevm-docs", "@hourly", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "sync-gdevm-docs") {
		t.Errorf("error should name the function: %v", err)
	}
}

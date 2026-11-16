package servicesv1

import "testing"

func TestValidateServiceName(t *testing.T) {
	valid := []string{"sshd.service", "nginx.service", "worker@blue.service", "my_api-2.service"}
	for _, name := range valid {
		if err := ValidateServiceName(name); err != nil {
			t.Errorf("ValidateServiceName(%q) error = %v", name, err)
		}
	}
	invalid := []string{"", "nginx", "-evil.service", "../nginx.service", "foo..bar.service", "foo/bar.service", "foo;id.service", " foo.service", "foo.service\n"}
	for _, name := range invalid {
		if err := ValidateServiceName(name); err == nil {
			t.Errorf("ValidateServiceName(%q) succeeded", name)
		}
	}
}

func TestValidateAction(t *testing.T) {
	for _, action := range []string{ActionStart, ActionStop, ActionRestart} {
		if err := ValidateAction(action); err != nil {
			t.Errorf("ValidateAction(%q) error = %v", action, err)
		}
	}
	for _, action := range []string{"", "enable", "disable", "reload", "daemon-reload", "start;id"} {
		if err := ValidateAction(action); err == nil {
			t.Errorf("ValidateAction(%q) succeeded", action)
		}
	}
}

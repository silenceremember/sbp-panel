package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type xrayRuntimeUser struct {
	ID    string
	Flow  string
	Email string
	Level int
}

type xrayRuntimeAPI struct {
	input   func(string, ...string) (string, error)
	command func(...string) (string, error)
}

func dockerXrayRuntimeAPI(container string) xrayRuntimeAPI {
	return xrayRuntimeAPI{
		input: func(input string, args ...string) (string, error) {
			command := append([]string{"exec", "-i", container, "xray", "api"}, args...)
			return runInput(input, "docker", command...)
		},
		command: func(args ...string) (string, error) {
			command := append([]string{"exec", container, "xray", "api"}, args...)
			return run("docker", command...)
		},
	}
}

func managedXrayInbound(root map[string]any) (map[string]any, string, error) {
	inbounds, _ := root["inbounds"].([]any)
	for _, raw := range inbounds {
		inbound, _ := raw.(map[string]any)
		if strings.TrimSpace(fmt.Sprint(inbound["protocol"])) != "vless" {
			continue
		}
		tag := strings.TrimSpace(fmt.Sprint(inbound["tag"]))
		if tag == "" || tag == "<nil>" {
			return nil, "", errors.New("the managed Xray VLESS inbound has no tag")
		}
		return inbound, tag, nil
	}
	return nil, "", errors.New("the managed Xray VLESS inbound was not found")
}

func xrayRuntimePayload(inbound map[string]any, users []xrayRuntimeUser) (string, error) {
	body, err := json.Marshal(inbound)
	if err != nil {
		return "", err
	}
	var candidate map[string]any
	if err := json.Unmarshal(body, &candidate); err != nil {
		return "", err
	}
	settings, _ := candidate["settings"].(map[string]any)
	if settings == nil {
		return "", errors.New("the managed Xray VLESS inbound has no settings")
	}
	clients := make([]any, 0, len(users))
	for _, user := range users {
		clients = append(clients, map[string]any{
			"id": user.ID, "flow": user.Flow, "email": user.Email, "level": user.Level,
		})
	}
	settings["clients"] = clients
	payload, err := json.Marshal(map[string]any{"inbounds": []any{candidate}})
	return string(payload), err
}

func xrayRuntimeEmails(api xrayRuntimeAPI, endpoint, tag string) (map[string]bool, error) {
	out, err := api.command("inbounduser", "--server="+endpoint, "-tag="+tag, "--json")
	if err != nil {
		return nil, fmt.Errorf("query Xray runtime users: %w", err)
	}
	var response struct {
		Users []struct {
			Email string `json:"email"`
		} `json:"users"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &response); err != nil {
		return nil, fmt.Errorf("decode Xray runtime users: %w", err)
	}
	emails := make(map[string]bool, len(response.Users))
	for _, user := range response.Users {
		emails[user.Email] = true
	}
	return emails, nil
}

func applyXrayRuntimeUsers(api xrayRuntimeAPI, root map[string]any, users []xrayRuntimeUser, enabled bool) error {
	if len(users) == 0 {
		return nil
	}
	inbound, tag, err := managedXrayInbound(root)
	if err != nil {
		return err
	}
	endpoint := xrayAPIEndpoint(root)
	if endpoint == "" {
		return errors.New("the managed Xray API endpoint was not found")
	}
	current, err := xrayRuntimeEmails(api, endpoint, tag)
	if err != nil {
		return err
	}
	pending := make([]xrayRuntimeUser, 0, len(users))
	for _, user := range users {
		if current[user.Email] != enabled {
			pending = append(pending, user)
		}
	}
	if len(pending) > 0 {
		if enabled {
			payload, err := xrayRuntimePayload(inbound, pending)
			if err != nil {
				return err
			}
			out, err := api.input(payload, "adu", "--server="+endpoint, "stdin:")
			if err != nil {
				return fmt.Errorf("add Xray runtime users: %w", err)
			}
			want := fmt.Sprintf("Added %d user(s) in total.", len(pending))
			if !strings.Contains(out, want) {
				return fmt.Errorf("Xray runtime added an unexpected number of users: %s", strings.TrimSpace(out))
			}
		} else {
			emails := make([]string, 0, len(pending))
			for _, user := range pending {
				emails = append(emails, user.Email)
			}
			sort.Strings(emails)
			args := []string{"rmu", "--server=" + endpoint, "-tag=" + tag}
			args = append(args, emails...)
			out, err := api.command(args...)
			if err != nil {
				return fmt.Errorf("remove Xray runtime users: %w", err)
			}
			want := fmt.Sprintf("Removed %d user(s) in total.", len(pending))
			if !strings.Contains(out, want) {
				return fmt.Errorf("Xray runtime removed an unexpected number of users: %s", strings.TrimSpace(out))
			}
		}
	}
	actual, err := xrayRuntimeEmails(api, endpoint, tag)
	if err != nil {
		return err
	}
	for _, user := range users {
		if actual[user.Email] != enabled {
			return fmt.Errorf("Xray runtime did not apply the user state for %s", user.Email)
		}
	}
	return nil
}

package headscale

import (
    "encoding/json"
    "fmt"
    "io"
    "log"
    "net/http"
    "strings"
    "time"
)

// FindMachineIPsByHostname queries a remote Headscale admin API (endpoint)
// for machines and returns IP addresses for machines matching the provided
// hostname or given_name. If token is non-empty, it is used as a Bearer token.
// The function is lenient: it fetches the machines list and filters locally.
// FindMachineIPsByHostname returns (ips, machineID, err)
func FindMachineIPsByHostname(endpoint, token, hostname string) ([]string, string, error) {
    endpoint = strings.TrimSpace(endpoint)
    if endpoint == "" {
        return nil, "", fmt.Errorf("empty headscale endpoint")
    }
    // Ensure a scheme and no trailing slash
    if !strings.Contains(endpoint, "://") {
        endpoint = "https://" + endpoint
    }
    base := strings.TrimRight(endpoint, "/")
    // Headscale typically exposes admin endpoints under /api/v1/machines
    u := base + "/api/v1/machines"
    client := &http.Client{Timeout: 10 * time.Second}
    req, err := http.NewRequest(http.MethodGet, u, nil)
    if err != nil {
        return nil, "", err
    }
    if token != "" {
        req.Header.Set("Authorization", "Bearer "+token)
    }
    resp, err := client.Do(req)
    if err != nil {
        return nil, "", err
    }
    defer resp.Body.Close()
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
        return nil, "", fmt.Errorf("headscale api %s returned %d: %s", u, resp.StatusCode, string(body))
    }
    var body []byte
    body, err = io.ReadAll(resp.Body)
    if err != nil {
        return nil, "", err
    }
    var machines []map[string]interface{}
    if err := json.Unmarshal(body, &machines); err != nil {
        // sometimes headscale wraps the machines under {"machines": [...]}
        var wrap map[string]any
        if err2 := json.Unmarshal(body, &wrap); err2 == nil {
            if v, ok := wrap["machines"]; ok {
                if arr, ok2 := v.([]any); ok2 {
                    machines = make([]map[string]interface{}, 0, len(arr))
                    for _, it := range arr {
                        if m, ok3 := it.(map[string]interface{}); ok3 {
                            machines = append(machines, m)
                        }
                    }
                }
            }
        }
        if len(machines) == 0 {
            log.Printf("headscale: failed to parse machines json: %v (raw=%s)", err, string(body))
            return nil, "", err
        }
    }
    hostname = strings.ToLower(strings.TrimSpace(hostname))
    ips := []string{}
    var machineID string
    for _, m := range machines {
        // match by hostname or given_name
        host := strings.ToLower(fmt.Sprint(m["hostname"]))
        given := strings.ToLower(fmt.Sprint(m["given_name"]))
        if hostname != "" && !strings.Contains(host, hostname) && !strings.Contains(given, hostname) {
            continue
        }
        // ip_addresses may be []string or JSON string
    if v, ok := m["ip_addresses"]; ok {
            switch t := v.(type) {
            case []any:
                for _, a := range t {
                    s := strings.TrimSpace(fmt.Sprint(a))
                    if s != "" {
                        ips = append(ips, s)
                    }
                }
            case string:
                // attempt JSON array parse
                var arr []string
                if json.Unmarshal([]byte(t), &arr) == nil {
                    for _, a := range arr {
                        a = strings.TrimSpace(a)
                        if a != "" {
                            ips = append(ips, a)
                        }
                    }
                } else {
                    for _, p := range strings.Split(t, ",") {
                        p = strings.TrimSpace(p)
                        if p != "" {
                            ips = append(ips, p)
                        }
                    }
                }
            default:
                s := strings.TrimSpace(fmt.Sprint(v))
                if s != "" {
                    ips = append(ips, s)
                }
            }
            // capture machine id if present
            if machineID == "" {
                if idv, ok := m["id"]; ok {
                    machineID = strings.TrimSpace(fmt.Sprint(idv))
                } else if idv, ok := m["machine_key"]; ok {
                    machineID = strings.TrimSpace(fmt.Sprint(idv))
                }
            }
        }
    }
    return ips, machineID, nil
}

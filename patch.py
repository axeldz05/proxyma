import re

with open("main_test.go", "r") as f:
    text = f.read()

# Replace http.Get(X.config.Address + ...) with X.server.Client().Get(...)
text = re.sub(r'http\.Get\(([a-zA-Z0-9_\[\]]+)\.config\.Address', r'\1.server.Client().Get(\1.config.Address', text)

# For POST/DELETE, they usually do:
# req, _ := http.NewRequest(..., X.config.Address + ...)
# resp, _ := http.DefaultClient.Do(req)
# We can find the `X` and replace.
lines = text.split('\n')
for i, line in enumerate(lines):
    if "http.DefaultClient.Do" in line:
        # looking backwards for the most recent sv, sv1, sv2, servers[0] used in NewRequest
        for j in range(i-1, i-20, -1):
            if j < 0: break
            m = re.search(r'NewRequest[^\n]+?([a-zA-Z0-9_\[\]]+)\.config\.Address', lines[j])
            if m:
                sv_var = m.group(1)
                lines[i] = line.replace('http.DefaultClient.Do', f'{sv_var}.server.Client().Do')
                break

with open("main_test.go", "w") as f:
    f.write('\n'.join(lines))

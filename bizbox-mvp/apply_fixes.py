import os

# 1. main.go - Add os/exec and GetIncusClient
with open("main.go", "r", encoding="utf-8") as f:
    main_data = f.read()

if '"os/exec"' not in main_data:
    main_data = main_data.replace('"os"', '"os"\n\t"os/exec"')

if 'func GetIncusClient()' not in main_data:
    main_data += """

// GetIncusClient returns a connected Incus client
func GetIncusClient() incus.InstanceServer {
\tsocketPath := "/var/lib/incus/unix.socket"
\tc, _ := incus.ConnectIncusUnix(socketPath, nil)
\treturn c
}
"""

with open("main.go", "w", encoding="utf-8") as f:
    f.write(main_data)

# 2. storage.go - fix variable assignment
with open("storage.go", "r", encoding="utf-8") as f:
    storage_data = f.read()

storage_data = storage_data.replace("err := c.CreateStoragePool(req)", "err = c.CreateStoragePool(req)")
storage_data = storage_data.replace('c := GetIncusClient()\n\tif c == nil {\n\t\treturn fmt.Errorf("Incus client error")\n\t}', 'socketPath := "/var/lib/incus/unix.socket"\n\tc, err := incus.ConnectIncusUnix(socketPath, nil)\n\tif err != nil {\n\t\treturn fmt.Errorf("Incus client error: %v", err)\n\t}')
storage_data = storage_data.replace('c := GetIncusClient()\n\tif c == nil {\n\t\treturn nil, fmt.Errorf("Incus client connection failed")\n\t}', 'socketPath := "/var/lib/incus/unix.socket"\n\tc, err := incus.ConnectIncusUnix(socketPath, nil)\n\tif err != nil {\n\t\treturn nil, fmt.Errorf("Incus client error: %v", err)\n\t}')

with open("storage.go", "w", encoding="utf-8") as f:
    f.write(storage_data)

# 3. network.go - add database/sql and pass vswitchName
with open("network.go", "r", encoding="utf-8") as f:
    network_data = f.read()

if '"database/sql"' not in network_data:
    network_data = network_data.replace('"encoding/json"', '"database/sql"\n\t"encoding/json"')

network_data = network_data.replace('return createOVSSegment(name, vlanID) //', 'return createOVSSegment(name, vlanID, vswitchName) //')
network_data = network_data.replace('return createOVSSegment(name, vlanID)\n}', 'return createOVSSegment(name, vlanID, vswitchName)\n}')

with open("network.go", "w", encoding="utf-8") as f:
    f.write(network_data)

# 4. uplink.go - remove encoding/json
with open("uplink.go", "r", encoding="utf-8") as f:
    uplink_data = f.read()

uplink_data = uplink_data.replace('\t"encoding/json"\n', '')

with open("uplink.go", "w", encoding="utf-8") as f:
    f.write(uplink_data)

print("All fixes applied!")

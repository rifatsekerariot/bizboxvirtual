import os, glob

for f in glob.glob("*.go"):
    with open(f, "r", encoding="utf-8") as file:
        data = file.read()
    data = data.replace("\"github.com/lxc/incus/client\"", "\"github.com/lxc/incus/v7/client\"")
    data = data.replace("\"github.com/lxc/incus/shared/api\"", "\"github.com/lxc/incus/v7/shared/api\"")
    with open(f, "w", encoding="utf-8") as file:
        file.write(data)

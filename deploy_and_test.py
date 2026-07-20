import paramiko
import os

host = '192.168.1.11'
user = 'admin'
password = 'admin'

local_tar = r'd:\Antigravity\bizboxvirtual\bizbox-mvp.tar.gz'
remote_tar = '/home/admin/bizbox-mvp.tar.gz'
remote_dir = '/home/admin/bizbox-mvp'

try:
    print(f"Connecting to {host}...")
    transport = paramiko.Transport((host, 22))
    transport.connect(username=user, password=password)
    
    print("Uploading file via SFTP...")
    sftp = paramiko.SFTPClient.from_transport(transport)
    sftp.put(local_tar, remote_tar)
    sftp.close()
    print("Upload completed.")
    
    print("Running SSH commands...")
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(host, username=user, password=password)
    
    commands = [
        f"sudo apt-get install -y build-essential",
        f"mkdir -p {remote_dir}",
        f"tar -xzf {remote_tar} -C {remote_dir}",
        f"cd {remote_dir} && export PATH=$PATH:/usr/local/go/bin && CGO_ENABLED=1 go build -o bizbox-mvp .",
        f"cd {remote_dir} && sudo date -s \"$(curl -s --head http://google.com | grep ^Date: | sed 's/Date: //g' | tr -d '\\r')\" && sed -i 's/\\r$//' install.sh && sudo bash ./install.sh"
    ]
    
    for cmd in commands:
        print(f"Executing: {cmd}")
        stdin, stdout, stderr = ssh.exec_command(cmd)
        
        # Wait for command to finish and print output
        exit_status = stdout.channel.recv_exit_status()
        out = stdout.read().decode('utf-8').strip()
        err = stderr.read().decode('utf-8').strip()
        
        if out:
            print(f"STDOUT: {out}")
        if err:
            print(f"STDERR: {err}")
            
        if exit_status != 0:
            print(f"Command failed with exit status {exit_status}")
            break
            
    print("All commands executed.")
    ssh.close()
    transport.close()
except Exception as e:
    print(f"An error occurred: {e}")

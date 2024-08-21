# rathole-go

> 内网穿透工具，将内网的服务通过公网ip暴露出去。

使用 ratholego 需要一个有公网 IP 的服务器，和一个在 NAT 或防火墙后的设备，其中有些服务需要暴露在互联网上。

假设你在家里的 NAT 后面有一个 NAS，并且想把它的 ssh 服务暴露在公网上：

1. 在有一个公网 IP 的服务器上

创建 `server.toml`，内容如下，并根据你的需要调整。

```toml
# server.toml
[server]
bind_addr = "0.0.0.0:2333" # `2333` 配置了服务端监听客户端连接的端口

[server.services.my_nas_ssh]
token = "use_a_secret_that_only_you_know" # 用于验证的 token
bind_addr = "0.0.0.0:5202" # `5202` 配置了将 `my_nas_ssh` 暴露给互联网的端口
```

然后运行:

```bash
./ratholego --server server.toml
```

2. 在 NAT 后面的主机（你的 NAS）上

创建 `client.toml`，内容如下，并根据你的需要进行调整。

```toml
# client.toml
[client]
remote_addr = "myserver.com:2333" # 服务器的地址。端口必须与 `server.bind_addr` 中的端口相同。
[client.services.my_nas_ssh]
token = "use_a_secret_that_only_you_know" # 必须与服务器相同以通过验证
local_addr = "127.0.0.1:22" # 需要被转发的服务的地址
```

然后运行：

```bash
./ratholego --client client.toml
```

3. 现在 `rathole` 客户端会连接运行在 `myserver.com:2333`的 `rathole` 服务器，任何到 `myserver.com:5202` 的流量将被转发到客户端所在主机的 `22` 端口。

所以你可以 `ssh myserver.com:5202` 来 ssh 到你的 NAS。

## Configuration

如果只有一个 `[server]` 和 `[client]` 块存在的话，`rathole` 可以根据配置文件的内容自动决定在服务器模式或客户端模式下运行。

但 `[client]` 和 `[server]` 块也可以放在一个文件中。然后在服务器端，运行 `ratholego --server config.toml`。在客户端，运行 `ratholego --client config.toml` 来明确告诉 `rathole` 运行模式。

# AskFlow 使用说明

## 快速部署
```bash
./build.cmd
```

输入密码后自动完成：
1. 打包项目
2. 上传到服务器
3. 远程构建
4. 重启服务

## 命令行参数
```bash
./askflow -h
```

常用参数：
- `--port, -p` - 指定端口 (默认 8080)
- `--bind` - 绑定地址 (默认 0.0.0.0)
- `-4` - 强制 IPv4
- `-6` - 强制 IPv6

## 示例

```bash
# 指定端口
./askflow --port=3000

# 仅监听本地
./askflow --bind=127.0.0.1

# 强制 IPv6
./askflow -6
```

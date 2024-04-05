# 使用一个已有的镜像作为基础
FROM golang:1.21

# 设置工作目录
WORKDIR /app

# 将你的源代码和配置文件复制到工作目录
COPY . .

# 安装依赖
RUN go mod download

# 编译你的应用
RUN go build -o main .

# 设置启动命令
CMD ["./main"]

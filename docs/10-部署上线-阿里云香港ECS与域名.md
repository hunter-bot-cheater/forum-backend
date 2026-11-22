# 第 10 章 部署上线：阿里云香港 ECS + Docker + Nginx + 域名

这一章把项目从「本机能跑」变成「公网能访问」。目标状态：浏览器打开 `https://jiushixixi.xyz`，能看到论坛首页，能注册登录、发文章、收通知，证书自动续期，机器重启后服务自己回来。

写作顺序按你实际操作时会遇到的顺序来：先判断机器够不够，再买，再登录，再装环境，再拉代码，再配，再起，再配域名和证书，最后是日常运维和排错。每一段都说明「为什么这么做」，因为部署这类事情，照抄命令只能成功一次，理解原因才能在出问题时自己判断。

---

## 0. 先看结论

如果你的时间只够看一屏，看这里。

| 问题 | 结论 |
|---|---|
| 截图里这台机器能跑这个项目吗 | 能。选 **2 核 2G、经济型 e、40G ESSD Entry**，这就是截图第一行那台 |
| 免费额度够用多久 | 约 1796 小时，折合约 74 天，也就是 3 个月试用期里前 2.5 个月不花钱 |
| 选香港对不对 | 对。香港地域不需要 ICP 备案，买了当天就能绑域名 |
| 2G 内存会不会不够 | 跑起来够（常驻约 1G），**构建镜像**那几分钟是峰值，必须加 2G swap |
| 服务器构建太慢怎么办 | 本机交叉编译，服务器只打镜像，5 到 15 分钟变十几秒，见 13.5 |
| 部署方式 | Docker Compose，五个容器：mysql、redis、api、web（nginx）、pool-sim（台球物理） |
| 需要开哪些端口 | 只开 22、80、443。3306、6379、8080 一个都不要开 |
| 证书怎么来 | Let's Encrypt + certbot，90 天自动续期，免费 |
| 域名怎么指过来 | 阿里云「云解析 DNS」加两条 A 记录：`@` 和 `www` 都指向 ECS 公网 IP |

下面开始逐项解释。

---

## 1. 这台机器到底够不够

### 1.1 先算免费额度这笔账

截图里的规则是：**免费抵扣总额度 300 元，每小时抵扣上限 0.833 元，有效期 3 个月**。

这句话的意思是：机器按小时计费，每小时花多少钱，就从 300 元里扣多少，但每小时最多扣 0.833 元。所以你选的规格越便宜，能撑的小时数越多。

按截图里的单价算：

| 规格 | 单价（元/小时） | 300 元能跑多久 | 折合天数 | 3 个月试用期内是否够 |
|---|---|---|---|---|
| 2 核 2G 经济型 e | 0.167 | 约 1796 小时 | 约 74.8 天 | 够，还剩约 17 天要自己付 |
| 2 核 4G 经济型 e | 0.306 | 约 980 小时 | 约 40.8 天 | 不够，第 41 天开始付费 |
| 2 核 2G 通用算力型 u1 | 0.511 | 约 587 小时 | 约 24.5 天 | 不够 |
| 2 核 4G 通用算力型 u1 | 0.536 | 约 559 小时 | 约 23.3 天 | 不够 |
| 2 核 8G 经济型 e | 0.644 | 约 465 小时 | 约 19.4 天 | 不够 |
| 2 核 8G 通用算力型 u1 | 0.702 | 约 427 小时 | 约 17.8 天 | 不够 |

注意两件事：

1. **「免费可用时长 约 1796 小时」这一列是阿里云自己算的**，和上面的除法一致，说明它已经把 300 元除以单价了。但它不等于「3 个月全免」，3 个月是 2160 小时，多出来的 364 小时（约 15 天）要自己掏钱，按 0.167 元/小时算不到 61 元。
2. **免费额度通常只覆盖实例和系统盘，公网带宽/流量另算**。下单时一定要看订单页的明细，别以为 300 元把带宽也包了。

### 1.2 2G 内存怎么分配

这是最需要算清楚的一项。项目跑起来要同时装下这些东西：

| 组件 | 常驻内存（约） | 说明 |
|---|---|---|
| Ubuntu 24.04 系统本体 | 150 ~ 250 MB | sshd、systemd、日志服务等 |
| dockerd + containerd | 120 ~ 200 MB | 容器运行时，跑几个容器都要这些 |
| MySQL 8.0 | 350 ~ 500 MB | 按第 14 节的参数调过之后约 300 MB |
| Redis 7 | 15 ~ 30 MB | 数据量很小，主要是登录态登记表 |
| Go 后端（api） | 40 ~ 80 MB | 静态编译，没有 JVM 那种固定开销 |
| Nginx（web） | 10 ~ 20 MB | 静态文件 + 反代 |
| 台球物理服务（pool-sim） | 200 ~ 300 MB | Python + numba，满力开球那一杆会冲到上限 768 MB（见 12.15） |
| 合计 | **约 0.9 ~ 1.4 GB** | 常态；开球瞬间再高两三百兆 |

2G 里剩下 600MB 到 1.1GB，看起来还有余量。真正的风险不在「运行」，在**构建**：

- `docker compose build` 会编译 Go（链接阶段单个进程能吃到 500MB 以上）；
- 前端 `npm ci` + `vite build` 的 Node 进程峰值能到 1GB 以上；
- 这两件事同时发生的话，2G 内存会被打穿，Linux 的 OOM Killer 会直接杀掉进程，报错长这样：`signal: killed` 或者 `exit code: 137`。

台球那个 Python 容器还有一条自己的风险：numba 在第一次出杆时要现编译，满力开球那一刻的临时数组最占内存，所以它的上限设成 768M（512M 会在开球时被杀）。它是**按需**冲高的，不是常驻，但意味着这台机器在有人开球的那一秒余量会被吃掉不少——真到了内存紧张，先看 `docker stats --no-stream` 里它有没有常年贴着 768M，而不是先怀疑 Go。

所以第 5 节会让你先加 2G swap。swap 不是「内存不够就靠它跑」，它的作用是**把短暂的构建峰值扛过去**，别让编译进程被系统杀掉。运行期项目基本不会用到 swap，用到就说明该升级配置了。

### 1.3 CPU：经济型 e 是共享型

经济型 e 属于共享型实例，同一台物理机上的 vCPU 由多个用户分时使用，长时间满载会降频。表现出来的现象是：刚开机很快，编译几分钟后变慢。

对这个项目没影响：论坛的请求是「等一下数据库、拼一下 JSON」，CPU 占用极低，降频也够用。只有一种情况要注意：**别在这台机器上跑压测**，压测结果没有参考价值，还会把 CPU 积分（阿里云给共享型实例的突发额度）烧光，之后一段时间更慢。

通用算力型 u1 是独享型，稳定但贵三倍。初期没有必要。

### 1.4 磁盘和带宽

**磁盘**：40G ESSD Entry，系统占 8~10G，Docker 镜像和构建缓存占 3~8G，数据库和日志占 1~2G，够用。真正会失控的是 Docker 的构建缓存，每次改代码重新构建都会留一层，第 13 节给了清理命令。

**带宽**：这是新手最容易忽略的成本项。ECS 的公网带宽有两种计费方式：

| 方式 | 适合场景 | 说明 |
|---|---|---|
| 按固定带宽 | 访问量稳定 | 买 1~5 Mbps，超出部分靠排队，不会额外扣钱 |
| 按使用流量 | 访问量小且不稳定 | 只按实际出网流量收费，被刷流量时账单会很难看 |

论坛的文字页面压缩后首页约 300~500KB，图片走的是用户上传的头像（后端限制 1MiB），所以 **3 Mbps 固定带宽**足够几个朋友同时浏览。按流量计费更便宜但不可控，初期建议固定带宽。

### 1.5 结论

选 **2 核 2G 经济型 e，40G ESSD Entry，3 Mbps 固定带宽，Ubuntu 24.04**。

什么时候该换 2 核 4G：构建镜像每次都失败、`docker stats` 看到 MySQL 长期贴着上限、或者你打算在这台机器上同时跑 Prometheus 和 Grafana（这两个加起来又要 500MB）。单纯跑论坛，2G 够。

---

## 2. 为什么选香港

截图里那句提示是关键：

> 当前试用 ECS 为按量付费实例，不满足中国内地 ICP 备案要求，如需备案请前往购买页选购包年包月实例立即前往

拆开解释：

- **在中国大陆地域**（北京、杭州、广州、成都等）用域名对外提供网站服务，必须做 ICP 备案。备案的前提之一是有包年包月实例（通常要求 3 个月以上），按量付费的试用机不满足条件。
- **没备案的域名解析到大陆服务器，80 和 443 会被拦**。表现是域名打不开、IP 直接访问却能通，很多人卡在这里以为是 Nginx 配错了。
- **香港地域不属于大陆备案管辖范围**，买来就能绑域名、当天上线。这也是本项目选香港的核心理由：你要的是「先跑起来」。

代价也要知道：

- 大陆访问香港的延迟大约 30~60ms（对比大陆同城 5~20ms），跨境出口在晚高峰可能抖动。论坛这种以文字为主的站点，用户基本感知不到。
- 香港机器的公网带宽通常比大陆贵一些，下单时看清价格。
- 如果以后要迁到大陆，**必须**先备案，而且备案期间网站不能开。这是后话。

---

## 3. 下单：每一项怎么选

在阿里云 ECS 试用页面（你截图那一页）按下面选：

| 选项 | 选什么 | 为什么 |
|---|---|---|
| 地域 | **中国香港** | 免备案，见第 2 节 |
| 实例规格 | **2 核 2G，经济型 e** | 见第 1 节算的账 |
| 镜像 | **Ubuntu 24.04 LTS 64 位** | 长期支持到 2029 年；Docker 官方文档和社区教程最多 |
| 系统盘 | 40G ESSD Entry（试用自带） | 够用，见 1.4 |
| 公网 IP | 分配公网 IPv4 | 没有公网 IP 就没法绑域名 |
| 带宽计费 | 固定带宽 3 Mbps | 见 1.4 |
| 登录凭证 | **密钥对**，没有就新建 | 比密码安全得多，且不怕被爆破 |
| 安全组 | 先建一个，规则见第 4 节 | 默认规则常常开得太多 |

关于镜像，别选 CentOS 7：它已经停止维护（2024 年 6 月 EOL），装 Docker 会遇到一堆过期的源。也别选 Alibaba Cloud Linux，虽然性能不错，但网上教程少，出问题不好搜。

创建密钥对时会让你下载一个 `.pem` 文件，**这个文件只能下载一次**，丢了就得重新创建密钥对并重启实例。下载后放到一个不会误删的目录，Windows 上建议放 `C:\Users\你的用户名\.ssh\`。

---

## 4. 安全组：只开三个端口

安全组就是云上的防火墙。入方向（别人访问你）只需要这三条：

| 端口 | 协议 | 授权对象 | 用途 | 不开的后果 |
|---|---|---|---|---|
| 22 | TCP | 你的家庭/公司公网 IP，形如 `1.2.3.4/32` | SSH 登录 | 连不上服务器 |
| 80 | TCP | `0.0.0.0/0` | HTTP，以及证书签发验证 | 域名打不开，证书申请失败 |
| 443 | TCP | `0.0.0.0/0` | HTTPS | 站点打不开 |

其余端口一律不开。特别是这三个，开了就是给攻击者送入口：

| 端口 | 开了会怎样 |
|---|---|
| 3306（MySQL） | 公网上的自动化脚本会持续爆破 root 密码。弱口令几分钟就能进库，全站数据被拖走 |
| 6379（Redis） | 未授权访问是经典漏洞，攻击者可以直接往 Redis 写 SSH 公钥，拿到服务器 root |
| 8080（后端） | 绕过 Nginx 就绕过了限流、TLS 和访问日志，还能直接打 `/metrics` 看你的内部数据 |

在本项目的 compose 里，后端**根本没有映射端口到宿主机**（`expose: 8080` 只对容器网络可见），MySQL 和 Redis 也没有映射，所以即使你误开了 3306，外面也连不上。这是第二层保险，但安全组这一层仍然要收紧。

SSH 那条把来源写成 `1.2.3.4/32` 而不是 `0.0.0.0/0`：`/32` 表示只允许这一个 IP。如果你家的公网 IP 会变（家宽常见），某天连不上就把规则改成新 IP，或者临时放开再关掉。这一步能挡掉绝大部分 SSH 爆破，日志会干净很多。

---

## 5. 第一次登录与系统初始化

### 5.1 用密钥登录

在你自己电脑的终端里执行（把 IP 换成你的公网 IP）：

```bash
ssh -i ~/.ssh/jiushixixi.pem root@8.8.8.8
```

Windows 的 PowerShell 里路径写法不同：

```powershell
ssh -i C:\Users\admin\.ssh\jiushixixi.pem root@8.8.8.8
```

如果报 `WARNING: UNPROTECTED PRIVATE KEY FILE` 或 `Permission denied (publickey)`，是权限太宽，Windows 上执行：

```powershell
icacls C:\Users\admin\.ssh\jiushixixi.pem /inheritance:r /grant:r "$($env:USERNAME):(R)"
```

这条命令的意思是：去掉从上级目录继承来的权限（`/inheritance:r`），只给当前用户读权限（`(R)`）。SSH 要求私钥只能被所有者读取，否则任何人都能拿它登录。

阿里云的 Ubuntu 镜像默认用户是 `root`。如果连不上，试试 `ubuntu@`，用哪个用户取决于镜像，控制台的「远程连接」按钮里能看到默认用户名。

第一次连接会问 `Are you sure you want to continue connecting?`，输入 `yes`。这是把服务器的指纹记到 `~/.ssh/known_hosts`，之后如果指纹变了会警告你（可能是中间人攻击）。

### 5.2 更新系统、设置时区

```bash
apt update && apt -y upgrade
```

`apt update` 只更新「软件包清单」，不装任何东西；`apt upgrade` 才是按清单升级。两条必须连起来用，只跑 upgrade 会装到旧版本。

```bash
timedatectl set-timezone Asia/Shanghai
timedatectl
```

**时区这一条很重要**。后端的数据库连接串用的是 `loc=Local`（见 `internal/config/db.go`），意思是「按进程所在的时区解释时间」。服务器默认是 UTC，不设时区的话，库里存的 `created_at`、日志里的时间、页面上显示的「3 分钟前」全都会差 8 小时。

输出里看到 `Time zone: Asia/Shanghai (CST, +0800)` 就对了。

顺手装两个后面要用的工具：

```bash
apt -y install git curl ca-certificates
```

`ca-certificates` 是根证书，缺了它容器里调 HTTPS 接口（比如大模型）会握手失败。

### 5.3 加 2G swap

```bash
fallocate -l 2G /swapfile
chmod 600 /swapfile
mkswap /swapfile
swapon /swapfile
echo '/swapfile none swap sw 0 0' >> /etc/fstab
free -h
```

逐条解释：

- `fallocate -l 2G /swapfile`：快速创建一个 2G 的空文件。它不写真实数据，只分配空间，所以瞬间完成。
- `chmod 600`：只有 root 能读写。swap 文件里可能有数据库密码之类的内存残留，不能让别的用户读。
- `mkswap`：把这个文件格式化成 swap 分区。
- `swapon`：立即启用，本次开机生效。
- `echo ... >> /etc/fstab`：写进开机挂载表，否则重启后 swap 就没了。这一条最容易漏。
- `free -h`：确认 Swap 那一行显示 2.0Gi。

`swapon` 之后可以用 `swapon --show` 看当前启用的 swap。

### 5.4 安装 Docker

```bash
curl -fsSL https://get.docker.com -o /tmp/get-docker.sh
sh /tmp/get-docker.sh
```

这个脚本做的事，比我在这里手写一堆 `apt` 命令更可靠：

1. 添加 Docker 官方的 apt 软件源（带 GPG 签名校验，防止源被替换）；
2. 安装 `docker-ce`、`docker-ce-cli`、`containerd.io`、`docker-buildx-plugin`、`docker-compose-plugin`；
3. 配置 systemd 让 Docker 开机自启。

为什么不用 `apt install docker.io`：Ubuntu 仓库里那个版本旧，而且**不带 `docker compose` 子命令**（v2 版本），只能装老的 `docker-compose`（v1，Python 写的，已经停止维护）。本项目的 `docker-compose.yml` 用的是 v2 语法（`name`、`depends_on.condition`），v1 解析不了。

香港机器可以直接连 Docker Hub 和官方源，不需要配镜像加速。如果你以后把机器搬到大陆，拉镜像会超时，那时再配 `registry-mirrors`。

验证：

```bash
docker version
docker compose version
```

第二条要看到 `Docker Compose version v2.2x.x` 以上。特别注意：**要 ≥ 2.24.4**（2023 年底的版本），因为第 9 节的 `deploy/prod/docker-compose.prod.yml` 用了 `!override` 标记（原因在 9.1 解释）。看版本号不够的话，执行 `apt -y install docker-compose-plugin` 升级。

### 5.5 建工作目录

```bash
mkdir -p /opt/forum
```

为什么放 `/opt`：这是 Linux 存放「额外安装的第三方软件」的约定位置，不在 `/root` 下面。放 `/root` 的问题是很多备份、清理脚本默认跳过 `/root`，而且日后想开个普通用户来管理项目时，`/root` 权限不对。

---

## 6. 把代码拉到服务器

### 6.1 为什么不用 HTTPS + 密码

用 `git clone https://github.com/...` 拉私有仓库会要求输入账号密码，而 GitHub 早就关闭了密码认证，只能用 Personal Access Token（PAT）。PAT 的问题是：它以明文存在 `.git/config` 或 credential helper 里，权限是「你账号的全部仓库」，泄露一次影响面很大。

服务器上更合适的做法是 **Deploy Key**：一对只属于这台机器的 SSH 密钥，只对**一个仓库**有**只读**权限，泄露了也只影响这一个仓库，随时可以删掉。

### 6.2 生成部署密钥

在服务器上执行：

```bash
ssh-keygen -t ed25519 -C "forum-deploy" -f ~/.ssh/forum_deploy -N ""
cat ~/.ssh/forum_deploy.pub
```

逐项解释：

- `-t ed25519`：用 Ed25519 算法。它比 RSA 短得多、快得多，现在的 GitHub 完全支持，不再需要用 4096 位 RSA。
- `-C "forum-deploy"`：注释，会附在公钥末尾，方便你在 GitHub 的列表里认出这是哪台机器的密钥。
- `-f ~/.ssh/forum_deploy`：指定文件名。不指定的话会覆盖默认的 `id_ed25519`，那个可能已经用于别处。
- `-N ""`：空密码。服务器上的密钥如果用密码保护，每次 `git pull` 都要人工输入，自动化就没法做了。安全性由「文件权限 600」+「只读权限」保证。

`cat` 出来的那一行以 `ssh-ed25519` 开头，整行复制。

### 6.3 在 GitHub 上添加 Deploy Key

打开仓库页面，路径是：

`Settings` → 左侧 `Deploy keys` → `Add deploy key`

- Title 填 `hk-ecs`（自己认得就行）
- Key 粘贴刚才复制的那一行
- **不要勾 `Allow write access`**。服务器只需要读代码，给它写权限意味着服务器被入侵时攻击者能改你的仓库。

### 6.4 让 git 自动使用这把密钥

```bash
cat >> ~/.ssh/config <<'EOF'
Host github.com
  HostName github.com
  User git
  IdentityFile ~/.ssh/forum_deploy
  IdentitiesOnly yes
EOF
chmod 600 ~/.ssh/config
```

为什么要写 `config` 而不是每次 `GIT_SSH_COMMAND=...`：写进配置后，`git clone`、`git pull`、`git fetch` 全都自动用这把密钥，不用记参数。`IdentitiesOnly yes` 的作用是「只用指定的这把」，否则 SSH 会把 `~/.ssh` 下所有密钥挨个试一遍，GitHub 对失败次数有限制，试多了会被临时封。

然后克隆（把 `<你的用户名>/<仓库名>` 换成实际地址）：

```bash
git clone git@github.com:<你的用户名>/<仓库名>.git /opt/forum
cd /opt/forum
git log --oneline -3
```

看到提交记录就说明密钥生效了。

### 6.5 以后的更新方式

```bash
cd /opt/forum
git pull --ff-only
```

`--ff-only` 的意思是「只接受快进合并」。服务器上不应该产生合并提交：如果你在服务器上改过代码又 pull，Git 会尝试合并，冲突时没人能处理。加了这个参数，一旦分叉就直接报错，逼你先搞清楚状况。

配置文件 `config/config.yaml` 和 `.env` 都被 `.gitignore` 忽略，`git pull` 不会覆盖它们，所以服务器上改过的配置是安全的。

---

## 7. 配置文件：改哪几处，为什么

项目里有两处配置：`.env`（给 Docker Compose 读）和 `config/config.yaml`（给 Go 后端读）。两者职责不同，别混。

### 7.1 `.env`：密码与密钥

```bash
cd /opt/forum
cp .env.example .env
openssl rand -hex 16    # 生成 MySQL 业务用户密码
openssl rand -hex 16    # 生成 MySQL root 密码
openssl rand -hex 16    # 生成 Redis 密码
openssl rand -hex 32    # 生成 JWT_SECRET
```

`openssl rand -hex 16` 生成 32 个十六进制字符（128 位随机数）。为什么用 `-hex` 不用 `-base64`：base64 会出现 `/`、`+`、`=` 这些字符，嵌进数据库连接串或 YAML 时需要转义，容易出错；hex 只有 `0-9a-f`，放哪都安全。

`JWT_SECRET` 用 `-hex 32`，得到 64 个字符。服务端强制要求至少 32 个字符，公开的占位值会被拒绝启动。

用 `vi` 或 `nano` 打开 `.env`，把四个 `change_me_*` 换成刚生成的值：

```bash
vi .env
```

文件内容的含义：

| 变量 | 作用 | 填错会怎样 |
|---|---|---|
| `MYSQL_ROOT_PASSWORD` | MySQL 超级用户密码，只用于容器内部健康检查 | 健康检查失败，api 起不来 |
| `MYSQL_PASSWORD` | 业务用户 `forum` 的密码 | 后端连不上库，日志报 `Access denied` |
| `REDIS_PASSWORD` | Redis 密码 | 后端连不上 Redis，登录态和限流全挂 |
| `JWT_SECRET` | 签发 token 的密钥 | 太短会拒绝启动；泄露等于别人能伪造任何人的登录态 |
| `LLM_ENABLED` / `LLM_API_KEY` | 自动打标签功能 | 不填就是关闭，发布文章不发外部请求 |
| `NGINX_CONF` | 生产用哪份 Nginx 配置 | 见第 10 节，先填 `nginx.http.conf` |

**首次部署时先把 `NGINX_CONF` 改成 `nginx.http.conf`**，原因在第 10 节，现在改好省得来回切。

### 7.2 `config/config.yaml`：业务配置

```bash
cp config/config.docker.yaml config/config.yaml
```

为什么复制这份而不是 `config.example.yaml`：`config.docker.yaml` 里的地址已经指向 compose 的服务名（`mysql`、`redis`），并且把 `auto_migrate` 关掉了（建表交给独立的 migrate 容器）。用 `config.example.yaml` 的话，你得自己改一遍 host。

然后编辑这个文件，**必须改两处**：

**第一处，`ws.allowed_origins`。**

```yaml
ws:
  allowed_origins:
    - "127.0.0.1:8088"
    - "localhost:8088"
    - "jiushixixi.xyz"
    - "www.jiushixixi.xyz"
```

这是 WebSocket 的跨站防护。后端在握手时检查浏览器发来的 `Origin` 头（`internal/ws/ws.go` 里的 `checkOrigin`），不在白名单里的一律拒绝。浏览器访问 `https://jiushixixi.xyz` 时，`Origin` 就是 `https://jiushixixi.xyz`，去掉协议后是 `jiushixixi.xyz`，注意**不带端口**（443 是 https 默认端口，浏览器不写）。

不加这两条会怎样：页面能打开、能登录，但通知角标不更新、私信收不到实时推送，控制台会报 WebSocket 403。这个故障很难猜，因为你不会想到是「域名不在白名单」。

**第二处，`admin.user_ids`。**

```yaml
admin:
  user_ids: [1]
```

这是管理员白名单，填的是 `users` 表的自增 id，不是用户名。填 1 表示 id 为 1 的用户能进 `/admin`、能处置举报和改标签。

为什么这一条必须改文件、不能用环境变量：viper 把环境变量读进来时是字符串，不会自动拆成数组，列表类型的配置只能用文件。这也是第 9 节把 `config.yaml` 挂载进容器的原因。

id 怎么查：注册完账号后，用你的账号调一次 `GET /api/users/me`，响应里的 `id` 就是。也可以在服务器上直接查库：

```bash
cd /opt/forum
export COMPOSE_FILE=/opt/forum/docker-compose.yml:/opt/forum/deploy/prod/docker-compose.prod.yml
docker compose exec -T \
  -e MYSQL_PWD="$(grep '^MYSQL_ROOT_PASSWORD=' .env | cut -d= -f2)" \
  mysql mysql -uroot forum -e "SELECT id, username FROM users ORDER BY id LIMIT 5;"
```

`-e MYSQL_PWD=...` 是把密码通过环境变量传给容器内的 mysql 客户端，不用 `-p` 写在命令行上。命令行上的密码会被同机的其他用户通过 `ps` 看到，容器内外都是一个道理。`-T` 是不分配终端，这种一次性查询不需要交互。

其余配置保持默认即可。几个值得知道的：

- `server.trusted_proxies: ["172.28.0.0/16"]` 与 compose 里写的子网对应，让后端能从 `X-Forwarded-For` 里取真实客户端 IP。改子网就要同步改这里，否则按 IP 限流会把所有访客算成同一个 IP，十个人就能把限流打满。
- `rate_limit.capacity: 10`、`rate: 2` 表示每个 IP 每秒补充 2 个令牌、最多攒 10 个。这是给小站点的保守值。
- `log.format: json`：生产用 JSON，方便以后接日志系统检索；本机开发用 `text` 更好读。

### 7.3 收紧文件权限

这两个文件含数据库密码与 JWT 密钥，默认 644 在多用户机器上等于公开。但它们的读者不是同一个人，权限不能一刀切：

```bash
chmod 600 .env
chown 10001:10001 config/config.yaml
chmod 640 config/config.yaml
ls -l .env config/config.yaml
```

- `.env` 只给 Docker Compose 读，compose 是 root 跑的，`600` 就够，别的进程不用读它。
- `config/config.yaml` 要挂进 api 容器（`./config/config.yaml:/app/config/config.yaml:ro`），而容器里的进程以 `uid 10001`（`forum`）运行。写成 `600 root:root` 的话容器内读不到，api 一启动就报 `加载配置失败: read config: open /app/config/config.yaml: permission denied`，然后反复重启。`640` + 属主 10001 的效果是「root 能改、容器能读、其他用户读不到」。

用 `vi` 改完配置后要再 `chown 10001:10001 config/config.yaml` 一次。`vi` 保存是「写临时文件再改名」，新文件的属主变成 root，下次重启 api 又会 permission denied。判断方法：

```bash
stat -c '%U:%G %a %n' config/config.yaml
# 要的是 forum:forum 640（uid 10001 在容器外没有用户名时会显示 10001:10001）
```

---

## 8. 首次构建与启动

### 8.1 设置 compose 文件组合

生产环境要在基础 compose 之上叠加生产配置，每次敲两个 `-f` 太啰嗦，用环境变量固定下来：

```bash
export COMPOSE_FILE=docker-compose.yml:deploy/prod/docker-compose.prod.yml
```

`COMPOSE_FILE` 是 Docker Compose 认的环境变量，用冒号分隔多个文件（Windows 上是分号，服务器是 Linux 用冒号）。设置之后，`docker compose up -d` 等价于 `docker compose -f docker-compose.yml -f deploy/prod/docker-compose.prod.yml up -d`。

写进 `~/.bashrc` 让它长期生效：

```bash
echo 'export COMPOSE_FILE=/opt/forum/docker-compose.yml:/opt/forum/deploy/prod/docker-compose.prod.yml' >> ~/.bashrc
source ~/.bashrc
```

注意这里写的是绝对路径。相对路径是相对「当前目录」解析的，你在别的目录执行 `docker compose` 就会找不到文件，报 `no configuration file provided`。

如果你打算按 13.5 的方式部署（本机编译、服务器只打镜像），`COMPOSE_FILE` 还要再加第三份 `deploy/prod/docker-compose.artifact.yml`。13.5 第四步有完整命令，这里的写法不用推翻，只是末尾多一段路径。

还要记住一件事：**`~/.bashrc` 只对交互式登录的 shell 生效，cron 不读它**。所以写进 crontab 的命令必须自己带上 `COMPOSE_FILE`（见 13.2 的例子），否则定时任务会报找不到配置文件，而且你收不到任何提示。

先看一眼合并后的结果，确认端口和挂载都对：

```bash
docker compose config | head -60
```

`docker compose config` 会把多个文件合并、把变量替换成真实值，然后打印最终配置。**这是排查「我明明配了怎么没生效」最快的手段**，比猜快得多。

### 8.2 构建镜像

> **先看 13.5**。服务器是 2 核 2G 的话，建议走 13.5：本机编译好、服务器只打镜像，把这一节的 5 到 15 分钟压到十几秒，也不会被 OOM 打断。走 13.5 就整个跳过这一节，构建命令换成 13.5 第五步的 `docker compose build api web`；不过下面 `uploads` 目录那段仍然要做。

```bash
cd /opt/forum
docker compose build
```

这一步会用根目录的 `Dockerfile` 构建 api 镜像（多阶段：golang 编译，再拷进 alpine 运行），用 `deploy/web.Dockerfile` 构建 web 镜像（node 构建静态文件，再拷进 nginx）。

第一次构建在 2G 机器上大约 5~15 分钟，取决于 CPU 被限到什么程度。构建过程中可以用另一个终端 `free -h` 看 swap 有没有被吃掉，被吃掉是正常现象。

如果中途报 `signal: killed` 或 `exit code: 137`，是内存不够，处理办法见第 15 节。

这条命令会构建四个服务：`api` 与 `migrate`（用同一份 `Dockerfile`，实际只出一份镜像）、`web`，以及台球的物理服务 `pool-sim`。最后这个不在「本机编译」那套链路里（它是 Python 源码，构建上下文就是仓库里的 `pool-sim/`），所以无论走 8.2 还是 13.5，**服务器上都得单独构建一次**：

```bash
docker compose build pool-sim
```

装 numpy、numba、pooltool 都是现成的 manylinux wheel，不需要 gcc，2 核 2G 上大约两三分钟。之后只有 `pool-sim/` 目录有改动时才需要重建，日常发版碰不到它。它的运行与排错见 13.6。

构建前先建好放用户头像的目录，并把属主改成容器里跑服务的那个用户：

```bash
mkdir -p uploads
sudo chown -R 10001:10001 uploads
```

`10001` 是 `Dockerfile` 里 `adduser -D -u 10001 forum` 给容器用户定的 uid。api 进程以非 root 运行，目录要是留在 `root:root` 的 755 上，它创建 `avatars/2026/11/` 这一步就会 `permission denied`，上传接口直接 500。

这个坑只在 Linux 服务器上出现：本机 Docker Desktop（Windows / macOS）的 bind mount 不校验 POSIX 权限，本地传得好好的，一上服务器就不行。少了 `chown`，第 12 节「头像能上传」那条会红，排查时还得进容器 `ls -l` 才看得出来。

compose 里把它挂给了 api（可写）和 web（只读，Nginx 直接从盘上吐文件）。不自己建目录的话 Docker 会用 root 创建一次，同样得补这条 `chown`。

这个目录**不在 git 里**（`.gitignore` 已忽略），也不会随镜像一起打包——它是运行时数据，重建镜像、`git pull` 都不会动它。

### 8.3 先起依赖，再迁移，最后起全量

分三步是有原因的，别图省事直接 `up -d`：

```bash
docker compose up -d mysql redis
```

先起数据库和 Redis。`docker compose ps` 会显示 `healthy`，这是健康检查通过的意思。MySQL 第一次初始化大约要 20~40 秒，`ps` 里显示 `starting` 时别急着下一步。

```bash
docker compose run --rm migrate
```

跑一次建表。`run` 会用 migrate 服务起一个**临时**容器执行 `/app/forum-migrate`，跑完退出；`--rm` 让它在退出后把容器删掉，不然 `docker ps -a` 里会攒一堆已退出的容器。

为什么建表要单独跑、不放在 api 启动时：多个副本同时启动会并发执行 AutoMigrate，MySQL 的 DDL 会互相锁表，严重时能锁死。所以 `config.docker.yaml` 里 `auto_migrate: false`，建表只由这个一次性任务做。重复执行是安全的，AutoMigrate 幂等。

看到 `migration done` 之类的日志并且退出码是 0 就成功了。

```bash
docker compose up -d
```

起剩下的 api 和 web。`-d` 是后台运行。

### 8.4 看日志、探活

```bash
docker compose ps
```

五个容器都应该是 `Up`，mysql、redis、pool-sim 后面跟 `(healthy)`。web 和 api 没有健康检查，显示 `Up` 就行。

`pool-sim` 的 `start_period` 给了 40 秒：容器启动后要先跑一杆满台开球做 JIT 预热（约五秒），numba 编译期间 `/healthz` 还不通，这段时间显示 `health: starting` 是正常的。三十秒后还是 `unhealthy` 就去看 `docker compose logs pool-sim`。

```bash
docker compose logs --tail=50 api
```

`--tail=50` 只看最后 50 行。`-f` 是持续跟随（相当于 `tail -f`），调试时用，看完按 `Ctrl+C` 退出，不会停掉容器。

后端启动成功的日志里会有一行监听 8080 的记录。如果看到 `Access denied for user`，是 `.env` 里 `MYSQL_PASSWORD` 和数据库实际密码不一致（改过 `.env` 但没重建 mysql 容器，数据卷里还是旧密码）。

探活：

```bash
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1/readyz
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1/healthz
```

两个都返回 200 就说明「Nginx 通到后端，后端通到 MySQL 和 Redis」这条链路是完整的。

这里的 `http://` 只在这一节有效。此时 `NGINX_CONF` 还是 `nginx.http.conf`，80 端口直服。
第 10 节切到 `nginx.conf` 之后，80 端口除了证书验证路径全部 301 跳 HTTPS，
同样的命令会返回 301，那时要改成 `curl -sS -o /dev/null -w '%{http_code}\n' https://jiushixixi.xyz/readyz`。

`/healthz` 只证明进程活着；`/readyz` 会真的去 ping 数据库和缓存，所以它才是「可以接流量」的判断依据。

---

## 9. Nginx 配置逐段讲

生产用的文件都在 `deploy/prod/` 下，一共五个：

| 文件 | 挂到哪里 | 作用 |
|---|---|---|
| `docker-compose.prod.yml` | 作为 compose 叠加层 | 开 80/443、挂配置和证书、设时区、限日志体积 |
| `nginx.http.conf` | `/etc/nginx/conf.d/default.conf` | 申请证书前的临时配置，纯 HTTP |
| `nginx.conf` | 同上（之后替换掉上一份） | 正式的 HTTPS 配置 |
| `locations.conf` | `/etc/nginx/snippets/forum-locations.conf` | 两个 server 块共用的站点规则 |
| `my.cnf` | `/etc/mysql/conf.d/forum.cnf` | 2G 机器上的 MySQL 内存参数 |

### 9.1 为什么需要一份「临时配置」

Nginx 启动时会读 `ssl_certificate` 指向的文件，文件不存在就直接启动失败：

```
nginx: [emerg] cannot load certificate "/etc/letsencrypt/live/jiushixixi.xyz/fullchain.pem"
```

而证书要靠 certbot 申请，certbot 又要靠 Nginx 提供验证文件（第 10 节解释），于是形成死循环：没证书起不来，起不来申请不了证书。

解决办法是先跑一份不带 TLS 的配置，把站点和验证路径跑起来，拿到证书再换成正式配置。这就是 `nginx.http.conf` 的用途。

切换由 `.env` 里的 `NGINX_CONF` 控制，compose 里那一行是：

```yaml
- ./deploy/prod/${NGINX_CONF:-nginx.conf}:/etc/nginx/conf.d/default.conf:ro
```

`${NGINX_CONF:-nginx.conf}` 是「取环境变量 `NGINX_CONF`，没设就用 `nginx.conf`」的意思。`:ro` 表示只读挂载，容器内的进程改不了它，少一条被篡改的路径。

顺带说清 `!override` 那个标记。Compose 合并多个文件时，`ports` 这类多值字段默认是**拼接**（base 的 8088 会保留），所以生产配置里写了：

```yaml
ports: !override
  - "80:80"
  - "443:443"
```

`!override` 让这一项**替换**而不是拼接。不写的话最终会映射 8088、80、443 三个端口，虽然安全组挡着 8088，但端口白开着是没必要的暴露。这个标记是 Compose 2.24.4 才加的，第 5.4 节已经检查过版本。

### 9.2 upstream 为什么写服务名

```nginx
upstream forum_api {
    server api:8080;
    keepalive 16;
}
```

`api` 是 compose 里的服务名。Docker 自带 DNS，同一网络内容器可以用服务名互相解析，所以 Nginx 不需要知道后端容器的 IP。

`keepalive 16` 让 Nginx 和后端之间保持最多 16 条空闲长连接。没有它，每个请求都要新建一条 TCP 连接，还要经历三次握手，在限流接口上这些开销很可观。

**这里有一个必须记住的副作用**：Nginx 只在**启动时**解析一次域名，之后一直用缓存下来的 IP。当你 `docker compose up -d` 重建了 api 容器，它会拿到一个新 IP，而 Nginx 还在往旧 IP 发请求，结果就是 502。第 13.1 节的发版流程里，重建 api 之后一定要重建 web，原因就在这里。

### 9.3 WebSocket 为什么单独一段

```nginx
location /api/ws/ {
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;
    proxy_read_timeout 300s;
    ...
}
```

WebSocket 握手靠两个 HTTP 头完成协议升级：客户端发 `Upgrade: websocket`，服务端回 `101 Switching Protocols`。这两个头在 HTTP 协议里叫「逐跳头」（hop-by-hop），意思是只对当前这一段连接有效，**代理默认不转发**。不转发的话，后端收到的就是普通 GET 请求，直接返回 426 或 400，浏览器控制台报 `WebSocket connection failed`。

`proxy_http_version 1.1` 也是必须的：Nginx 反代默认用 HTTP/1.0，而协议升级是 1.1 才有的特性。

`proxy_read_timeout 300s` 是因为 WebSocket 是一条长期不关的连接。Nginx 默认 60 秒没有数据就断开，而本项目后端心跳是 54 秒一次，只差 6 秒余量，网络一抖动就会误判。300 秒足够容纳几次丢包。

### 9.4 `Connection ""` 是什么意思

```nginx
location /api/ {
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    ...
}
```

`Connection ""` 表示把这个头设成空值。为什么不能干脆不写：Nginx 反代时默认会给后端加上 `Connection: close`，等于每个请求用完就断开；显式置空才表示「这条连接可以复用」，9.2 配的 `keepalive 16` 也才有意义。反过来写成 `Connection close`，效果和默认一样，长连接池白配。

### 9.5 SPA 回退

```nginx
location / {
    try_files $uri $uri/ /index.html;
}
```

前端用的是 history 路由，地址是 `/articles/24`、`/admin` 这种。服务器上并没有 `articles/24` 这个文件，直接访问会 404。

`try_files` 的意思是「依次尝试：这个文件存在就返回它，这个目录存在就返回目录，都不存在就返回 `index.html`」。返回 `index.html` 之后，路由由浏览器里的 vue-router 接管，它看到地址是 `/articles/24` 就渲染详情页。

### 9.6 `/metrics` 为什么只给内网

```nginx
location = /metrics {
    allow 172.16.0.0/12;
    deny all;
    ...
}
```

`/metrics` 暴露的是路由清单、错误率、在线人数、goroutine 数量。这些数据对攻击者有用（能看出哪些接口在报错、有多少人在线），对公网访客没用。所以只允许 Docker 网段访问，以后 Prometheus 也放在同一个网络里时能直接抓。

`allow` 和 `deny` 是按顺序匹配的，`deny all` 放在最后表示「前面没放行的都拒绝」。

### 9.7 三个 proxy_set_header 的用途

```nginx
proxy_set_header Host $host;
proxy_set_header X-Real-IP $remote_addr;
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
```

- `Host`：把原始域名传给后端。后端生成绝对链接（比如 301 跳转）时要用。
- `X-Real-IP`：客户端真实 IP。Nginx 是直接连进来的那一方，它看到的 `$remote_addr` 就是访客 IP。
- `X-Forwarded-For`：一条链，格式是「客户端 IP, 代理1 IP, 代理2 IP」。`$proxy_add_x_forwarded_for` 会把已有的值加上当前 IP。

后端只信任 `trusted_proxies` 里的网段发来的这些头，所以外人伪造 `X-Forwarded-For` 改不了自己的限流 IP。这就是 7.2 里那个 `172.28.0.0/16` 的作用。

---

### 9.8 图片上传与省流量

上传的图片**由 Nginx 直接从磁盘读**，请求不会进 Go 进程。这一段在 `deploy/prod/locations.conf` 里：

```nginx
location /api/uploads/ {
    client_max_body_size 6m;
    proxy_request_buffering off;
    ...
}

location ^~ /uploads/ {
    alias /var/www/uploads/;
    add_header Cache-Control "public, max-age=31536000, immutable" always;
    open_file_cache max=2000 inactive=60s;
    sendfile on;
    tcp_nopush on;
}
```

逐条说清楚为什么：

- **`client_max_body_size 6m`**：站点级写的是 1m，而一张手机照片轻松超过它。不覆盖这一条，上传会在 Nginx 这一层就被 413 挡掉，后端日志里一片干净，你会以为前端没发请求。给 6m 是因为后端上限是 5 MB，多出来的 1 MB 留给 multipart 的分隔符和字段名。
- **`proxy_request_buffering off`**：默认行为是 Nginx 先把整个请求体收完、写到临时文件，再转给后端。一张 5 MB 的图等于写一次盘、读一次、再转发。关掉之后边收边转，少一次落盘。
- **`alias /var/www/uploads/`**：这是 web 容器里的路径，指向宿主机 `./uploads`（compose 里只读挂载）。后端写的是同一个目录，两边看到的是同一份文件。
- **`Cache-Control: immutable` + 一年**：这是省流量的核心。之所以敢设一年，是因为文件名是内容的 sha256 前 16 位——图一换，文件名就变，URL 就变，浏览器不可能拿旧缓存糊弄用户。没有这层保证就只能设短缓存，回访用户每次都要重新下载。
- **`open_file_cache`**：头像文件小、请求密，缓存文件句柄能省掉每次 `open`/`close` 的系统调用。`min_uses 1` 表示打开过一次就缓存。
- **`sendfile` + `tcp_nopush`**：文件内容不经用户态直接进内核发送缓冲；`tcp_nopush` 把响应头和首个文件块攒在一个包里发，小文件能少一个 RTT。
- **`access_log off`**：一屏十几张头像会把真正的页面请求冲掉，而这类静态请求的排障价值几乎为零。

为什么不用 `try_files $uri =404`：它和 `alias` 一起用有已知的路径拼接问题，文件不存在时 `alias` 自己就会返回 404。

**注意 `sendfile` 与 `tcp_nopush` 不能放在全局**：本项目只覆盖了 `conf.d/default.conf`，主配置是镜像自带的那份。写在 location 里既能生效，也不依赖基础镜像的默认值。

验证方法（部署完执行）：

```bash
# 1. 换一次头像，然后看响应头。应当有 immutable 且 max-age 是一年
curl -sI https://jiushixixi.xyz/uploads/avatars/2026/11/<文件名>.jpg | grep -i cache-control

# 2. 再请求一次，看 Nginx 的 access_log 里没有这条（上面关了日志）
#    以及直接用浏览器 DevTools 的 Network，第二次应当是 (disk cache)
```

按 860 px / 267 KB 的原图算：压完约 16 KB，一张图省下 250 KB 左右。首页若展示 20 个头像，首次访问少传约 5 MB，回访时是 0——这就是「上传即压缩 + 内容哈希 + 一年强缓存」三件事叠加的效果。

---

## 10. HTTPS：申请证书、切换、续期

### 10.1 先用 HTTP 跑起来

确认 `.env` 里是：

```
NGINX_CONF=nginx.http.conf
```

然后：

```bash
docker compose up -d --no-deps --force-recreate web
docker compose logs --tail=20 web
```

`--no-deps` 表示不连带重启 api、mysql（它们没变，重启只是浪费时间）；`--force-recreate` 是因为只改挂载的文件内容时，Compose 认为容器定义没变，不会自动重建。

这一步先解决域名能不能通的问题：

```bash
curl -I http://jiushixixi.xyz
```

看到 `HTTP/1.1 200 OK` 就说明 DNS 和 80 端口都通了。如果这时报 `Could not resolve host`，是第 11 节的 DNS 还没生效，先把那步做完。

### 10.2 申请证书

```bash
mkdir -p certbot/www certbot/conf

docker run --rm \
  -v /opt/forum/certbot/conf:/etc/letsencrypt \
  -v /opt/forum/certbot/www:/var/www/certbot \
  certbot/certbot certonly --webroot -w /var/www/certbot \
  -d jiushixixi.xyz -d www.jiushixixi.xyz \
  --email 你的邮箱@example.com --agree-tos --no-eff-email
```

逐段解释：

- `mkdir -p certbot/www certbot/conf`：建两个目录。`www` 放验证文件，`conf` 放证书。它们会被挂进容器，也被 compose 挂进 web 容器。为什么建在项目目录下而不是 `/etc/letsencrypt`：跟着项目走，备份和迁移时一起带走，也不会和系统里其他 certbot 冲突。
- `--rm`：容器跑完自动删除。签发是几分钟的事，没必要留着。
- `-v .../conf:/etc/letsencrypt`：证书写到宿主机的 `certbot/conf`。**这个目录里有私钥**，`.gitignore` 已经忽略了它。
- `-v .../www:/var/www/certbot`：验证文件的交换目录。certbot 往里写，Nginx 从里面读。
- `certonly`：只签发证书，**不改 Nginx 配置**。有些教程用 `--nginx`，那会让 certbot 自己改配置，改出来的东西和你手写的那份混在一起，以后很难维护。我们的配置是手写的，所以只用它签发。
- `--webroot -w /var/www/certbot`：用文件验证的方式证明「你控制这个域名」。certbot 会写一个随机文件到 `-w` 指定的目录，然后让 Let's Encrypt 的服务器通过 `http://你的域名/.well-known/acme-challenge/随机串` 去读。能读到，就证明域名确实指向这台机器。
- `-d jiushixixi.xyz -d www.jiushixixi.xyz`：两个域名签在**同一张证书**里。第一个是主域名，证书目录名就是它（`/etc/letsencrypt/live/jiushixixi.xyz/`），第 9 节的 `ssl_certificate` 路径也是按这个写的。
- `--agree-tos`：同意服务条款（必须）。
- `--no-eff-email`：不把邮箱交给 EFF 做订阅。这是隐私选择，不影响签发。

成功后会打印 `Congratulations! Your certificate and chain have been saved at ...`，并显示到期时间（90 天后）。

如果报 `Timeout during connect (likely firewall problem)`，按顺序查三件事：域名解析对不对、安全组 80 开没开、Nginx 有没有起来。

如果报 `Invalid response from ... 404`，是验证文件路径没对上：检查 `nginx.http.conf` 里那段 `location /.well-known/acme-challenge/ { root /var/www/certbot; }` 和挂载路径。

### 10.3 切换到 HTTPS

把 `.env` 里的 `NGINX_CONF` 改成正式配置：

```bash
sed -i 's/^NGINX_CONF=.*/NGINX_CONF=nginx.conf/' .env
docker compose up -d --no-deps --force-recreate web
docker compose logs --tail=20 web
```

`sed -i` 表示直接改文件。如果你不放心，用 `vi .env` 手动改也一样。

验证：

```bash
curl -I http://jiushixixi.xyz
```

现在应该返回 `301` 并带 `Location: https://jiushixixi.xyz/`。

```bash
curl -I https://jiushixixi.xyz
openssl s_client -connect jiushixixi.xyz:443 -servername jiushixixi.xyz </dev/null 2>/dev/null | openssl x509 -noout -dates -subject
```

第二条会打印证书的生效时间、过期时间和签发给谁。`subject` 里应该能看到 `CN = jiushixixi.xyz`。

浏览器里打开 `https://jiushixixi.xyz`，地址栏应该是锁形图标，点开能看到「证书有效」。

### 10.4 自动续期

Let's Encrypt 的证书 90 天过期，官方建议每 60 天左右续一次。用 crontab 自动化：

```bash
crontab -e
```

加入一行（注意整行是**一行**，中间不能换行）：

```
0 3 * * * docker run --rm -v /opt/forum/certbot/conf:/etc/letsencrypt -v /opt/forum/certbot/www:/var/www/certbot certbot/certbot renew --webroot -w /var/www/certbot --quiet && docker compose -f /opt/forum/docker-compose.yml -f /opt/forum/deploy/prod/docker-compose.prod.yml exec -T web nginx -s reload >> /var/log/certbot.log 2>&1
```

拆开看：

- `0 3 * * *`：每天凌晨 3 点跑。五个字段分别是分、时、日、月、周。
- `certbot renew`：检查所有证书，**剩余不到 30 天才真的续**，其余情况直接退出。所以每天跑一次不会有任何副作用，也不会触发 Let's Encrypt 的频率限制。
- `--webroot -w /var/www/certbot`：续期也走文件验证。这里必须用 webroot，不能用 standalone：standalone 模式会自己起一个临时服务器去占 80 端口，而线上 80 一直被 web 容器占着，会直接报 `Address already in use`。
- `--quiet`：只在出错时输出，避免每天往日志里写一行「没到续期时间」。
- `&&`：前一条成功才执行后一条。续期失败时不会去 reload，保留旧证书继续工作。
- `docker compose ... exec -T web nginx -s reload`：让 Nginx 重新加载证书。`exec` 是在已运行的容器里执行命令，`-T` 表示不分配终端（cron 环境没有终端，不加会报错）。`nginx -s reload` 是平滑重载，不会断开现有连接。
- `>> /var/log/certbot.log 2>&1`：把输出追加到日志文件。`2>&1` 表示把标准错误也重定向到同一个文件，不加的话错误信息会进 cron 的邮件，而你多半没配邮件。

手动测试一次续期流程（不会真的续，因为时间没到）：

```bash
docker run --rm -v /opt/forum/certbot/conf:/etc/letsencrypt -v /opt/forum/certbot/www:/var/www/certbot certbot/certbot renew --webroot -w /var/www/certbot --dry-run
```

`--dry-run` 会走完整流程但不写入正式证书，是验证「续期将来会不会成功」的标准做法。

### 10.5 备选：阿里云免费 SSL 证书

如果你不想折腾 certbot，阿里云也提供免费 DV 证书（在「数字证书管理服务」里申请）。流程是：申请 → 域名验证 → 下载 Nginx 格式 → 得到 `.pem` 和 `.key` 两个文件 → 放到 `/opt/forum/certbot/conf/` 之类的位置 → 改 Nginx 配置里的路径。

不推荐作为首选的原因：免费证书现在有效期只有 3 个月，到期要手动重新申请、重新下载、重新上传。一年四次，每次都要记得。certbot 配好之后基本不用管。

---

## 11. 域名解析：在阿里云上怎么操作

### 11.1 前置条件：实名认证

`.xyz` 域名注册后必须做实名认证，否则会被暂停解析（通常给 5 天宽限期）。在阿里云「域名」控制台能看到状态，显示「实名认证成功」才算完。这是工信部的规定，和服务器在哪无关。

### 11.2 添加两条 A 记录

1. 打开阿里云控制台，搜索「云解析 DNS」并进入。
2. 在域名列表里找到 `jiushixixi.xyz`，点右侧的「解析设置」。
3. 点「添加记录」，填第一条：

| 字段 | 值 |
|---|---|
| 记录类型 | `A` |
| 主机记录 | `@` |
| 解析线路 | 默认 |
| 记录值 | 你的 ECS 公网 IP（形如 `47.243.x.x`） |
| TTL | `600` |

4. 再点「添加记录」，填第二条，只把主机记录换成 `www`，记录值相同。

几个概念解释：

- **记录类型 A**：把域名映射到一个 IPv4 地址。另有 AAAA（IPv6）、CNAME（映射到另一个域名）、MX（邮件）。
- **主机记录 `@`**：代表域名本身，也就是 `jiushixixi.xyz`。填 `www` 代表 `www.jiushixixi.xyz`。填 `*` 是泛解析，所有子域名都指过来，初期不需要。
- **TTL 600**：这条记录在各层 DNS 缓存里存 600 秒（10 分钟）。调小改起来更快，但查询更频繁；调大省查询但改完要等更久。600 是折中值。
- 公网 IP 在 ECS「实例详情」页的「网络信息」里能看到，别填成私网 IP（`172.`、`10.`、`192.168.` 开头的一定是私网）。

### 11.3 为什么根域名用 A 而不是 CNAME

DNS 规范里，根域名（`jiushixixi.xyz`）不能有 CNAME 记录，因为它同时必须有 SOA 和 NS 记录，而 CNAME 要求「这个域名不能有其他任何记录」。填了会解析异常。

如果你以后想接到 CDN 或对象存储，那些服务给的是 CNAME 地址，只能配在 `www` 或子域名上，根域名要靠「显式 URL 转发」或者用 A 记录直接指 IP。

### 11.4 验证

在服务器或本机执行：

```bash
nslookup jiushixixi.xyz 223.5.5.5
nslookup www.jiushixixi.xyz 223.5.5.5
```

或者：

```bash
dig +short jiushixixi.xyz @223.5.5.5
```

`223.5.5.5` 是阿里云的公共 DNS，指定它查询能绕开你本机运营商的缓存。

返回的 IP 和你 ECS 的公网 IP 一致就说明解析生效了。刚加完记录通常 1 到 10 分钟生效，如果超过半小时还不通，检查是不是记录值写错、或者域名的 DNS 服务器没指向阿里云（在「域名」页面的「DNS 修改」里能看，正常情况是 `ns1.aliyun.com` 这类）。

一个小技巧：解析刚加完的时候，可以用 `curl --resolve` 临时绕过 DNS 测试服务器：

```bash
curl -I --resolve jiushixixi.xyz:80:<你的公网IP> http://jiushixixi.xyz
```

`--resolve` 告诉 curl「访问这个域名时直接用这个 IP，别查 DNS」，可以在等 DNS 生效期间先把 Nginx 和证书调通。

---

## 12. 上线验收清单

逐条跑一遍，全过才算部署完成。

| 检查项 | 命令 | 期望结果 |
|---|---|---|
| HTTP 跳转 | `curl -I http://jiushixixi.xyz` | `301` 且 `Location` 指向 https |
| HTTPS 可访问 | `curl -I https://jiushixixi.xyz` | `200` |
| 证书有效期 | `openssl s_client -connect jiushixixi.xyz:443 -servername jiushixixi.xyz </dev/null \| openssl x509 -noout -enddate` | 90 天内到期 |
| 证书域名 | 同上，`-subject` | 含 `jiushixixi.xyz` |
| 后端就绪 | `curl -sS -o /dev/null -w '%{http_code}\n' https://jiushixixi.xyz/readyz` | `200` |
| 接口通 | `curl -sS https://jiushixixi.xyz/api/articles?page=1&page_size=1` | JSON，含 `list` |
| 指标不对外 | `curl -sS -o /dev/null -w '%{http_code}\n' https://jiushixixi.xyz/metrics` | `403` |
| 未开端口 | 在你本机执行 `nc -zv <公网IP> 3306`（没有 nc 就用 `telnet`） | 连不上（超时） |
| 头像能上传 | 浏览器进「编辑资料」→ 上传一张手机照片 | 提示「已压缩：xxx KB → xx KB」，顶栏头像立刻变，刷新后仍在 |
| 图片强缓存 | `curl -sI https://jiushixixi.xyz/uploads/avatars/<年>/<月>/<文件名>.jpg` | 有 `Cache-Control: public, max-age=31536000, immutable` |
| 静态资源缓存 | `curl -I https://jiushixixi.xyz/assets/<某个js文件>` | `Cache-Control: ... immutable` |
| 前端路由 | 浏览器直接打开 `https://jiushixixi.xyz/admin` 再刷新 | 不 404 |
| WebSocket | 浏览器 F12 → Network → WS，登录后看连接状态 | `101 Switching Protocols` |
| 时间正确 | 发一篇文章，看列表里的时间 | 与当前时间一致，不差 8 小时 |
| 管理入口 | 用白名单账号登录，点右上角菜单 | 出现「管理后台」 |
| 图片上传 | 上传一张小于 1MB 的头像 | 成功；大于 1MB 应报 413 |
| 台球开房 | 浏览器打开 `/pool` | 显示「我的房间」、六位房号、对面是空位 |
| 房号进房 | 用第二个账号在 `/pool` 的「有房号？直接进」里输那六位 | 第二个账号的标题变成「你在对方的房间里」，房号一致；第一个账号两秒内看到对面坐了人 |
| 台球开局 | 两个账号各点「准备」 | 两边都跳到 `/pool/match/<id>`，座位 0 的一方显示「你」，另一方显示「对手回合」 |
| 台球仿真 | 对局页瞄准出杆 | 球真的动；若显示「球桌暂时算不动」说明 `pool-sim` 没起来（见 13.6） |
| 房间回收 | `docker compose exec -T mysql sh -c 'mysql -uroot -p"$MYSQL_ROOT_PASSWORD" forum -e "SELECT status, COUNT(*) FROM pool_matches GROUP BY status"'` | 只有 `waiting` / `playing` / `finished` / `expired` 四种；`waiting` 不应持续增长 |

第 8 项（端口未开）容易被忽略，但它验证的正是你安全组配对了没有。第 11 项如果失败，回到 7.2 检查 `ws.allowed_origins`。
台球那几项失败时先看 `docker compose logs --tail=100 api | grep pool`：开会话、抢座位、仿真失败都会留痕。

台球这几条验收天生要两个账号同时在线，而接口是**按 IP 限流**的（`config/config.yaml` 的 `rate_limit`，默认容量 10、每秒补 2）。同一台电脑开两个浏览器窗口，等于两个人共用一个桶，第二个一进大厅就可能弹「请求过于频繁，请 1 秒后重试」——这是限流在起作用，不是功能坏了。测的时候把容量放宽一点，验完改回来：

```yaml
rate_limit:
  capacity: 30
  rate: 5
```

改完 `docker compose restart api` 生效（这一项是标量，从配置文件读，不用重建镜像）。

---

## 13. 日常运维

### 13.1 发版流程

如果按 13.5 的方式部署，`git pull` 与 `docker compose build` 这一段要换成「本机打包 → scp → 服务器解包 → build」，13.5 的「以后发版」一节有完整命令。下面这套是在服务器上从源码编译的版本。

```bash
cd /opt/forum
git pull --ff-only
docker compose build api web      # 只重建变了的镜像
docker compose up -d --no-deps api
docker compose up -d --no-deps --force-recreate web
docker compose logs --tail=50 api
```

第三条和第四条的 `--no-deps` 很重要：不加的话 compose 会认为「api 依赖 mysql」，把 mysql 也重建一遍，数据卷虽然还在，但数据库要冷启动几十秒，用户会看到一片 504。

**第四条（重建 web）是必须的**，即使前端代码没改。原因在 9.2：Nginx 启动时把 `api` 解析成 IP 缓存起来，api 容器重建后 IP 变了，Nginx 不知道，继续往旧 IP 发请求，结果是全站 502。重建 web 让它重新解析一次，耗时一两秒。

如果改动了数据库模型（新增表、新增列），在 `up -d api` 之前插入一步：

```bash
docker compose run --rm migrate
```

滚动顺序是「先迁移，再换新版本」。反过来会让新代码访问不存在的列，直接报错。

如果只改了 Nginx 配置：

```bash
docker compose up -d --no-deps --force-recreate web
```

不用 `build`，因为配置是挂载进去的。

### 13.2 备份

项目自带 `scripts/backup.sh`，从容器里导出：

```bash
cd /opt/forum
BACKUP_MODE=docker DB_PASSWORD=$(grep '^MYSQL_ROOT_PASSWORD=' .env | cut -d= -f2) scripts/backup.sh
```

`BACKUP_MODE=docker` 让它连 compose 里的 mysql 容器；`grep ... | cut -d= -f2` 是从 `.env` 里取出密码值（`cut -d=` 以等号分隔，取第二段）。脚本会把 dump 压缩后写到 `backups/`，文件名带时间戳，并做一次完整性校验。

放进 crontab（`COMPOSE_FILE` 必须显式给，cron 不读 `.bashrc`）：

```
30 3 * * * cd /opt/forum && COMPOSE_FILE=/opt/forum/docker-compose.yml:/opt/forum/deploy/prod/docker-compose.prod.yml BACKUP_MODE=docker DB_PASSWORD=$(grep '^MYSQL_ROOT_PASSWORD=' .env | cut -d= -f2) scripts/backup.sh >> /var/log/backup.log 2>&1
```

**备份留在同一台机器上等于没备份**。机器被释放、磁盘损坏，备份一起没。至少每周把它拉到本地：

```bash
# 在你自己的电脑上执行
scp -i ~/.ssh/jiushixixi.pem root@<公网IP>:'/opt/forum/backups/*.sql.gz /opt/forum/backups/*.tar.gz' ./
```

更稳的做法是传到阿里云 OSS 或另一台机器上。

脚本还会把 `uploads/`（用户上传的头像）打成 `uploads-<时间戳>.tar.gz` 放在同一个目录。头像是用户数据，从数据库里重建不出来；虽然丢了只是让人重传一次，但真要挨个通知的时候就知道疼了。`UPLOAD_DIR` 可以覆盖它默认的 `uploads/`。

`certbot/conf` 目录也值得定期打包带走：里面有证书私钥，重建机器时可以直接放回去，省一次签发。

### 13.3 日志与磁盘

查看磁盘：

```bash
df -h /
docker system df
```

`docker system df` 会分别列出镜像、容器、数据卷、构建缓存占了多少。构建缓存通常最大。

清理（**注意第二条会删掉所有没在用的镜像**，建议先看 `docker images` 确认没有要保留的）：

```bash
docker builder prune -f --filter until=168h
docker image prune -f
```

`--filter until=168h` 表示只删 7 天前的缓存，刚构建的还留着，下次构建更快。

系统日志：

```bash
journalctl --disk-usage
journalctl --vacuum-size=200M
```

容器日志已经在 compose 里限制了（单个文件 10MB，最多 3 个），不会无限增长。这是 `deploy/prod/docker-compose.prod.yml` 里 `logging` 那几行的作用，不配的话 Docker 默认把容器的所有输出写进一个文件，几个月能把磁盘写满。

### 13.4 开机自启

Compose 里的 `restart: unless-stopped` 意思是「除非你手动 stop，否则挂了就重启，机器重启也跟着起」。Docker 服务本身由 systemd 管理，开机自启。所以**机器重启后不需要你做任何事**，等一两分钟就好。

验证一次：

```bash
reboot
# 等 1 分钟重新登录
docker compose ps
curl -I https://jiushixixi.xyz
```

### 13.5 本机编译、服务器只打镜像（推荐）

8.2 里那条 `docker compose build` 是在服务器上从源码编译：下 golang 镜像、拉 Go 模块、`go build`，再下 node 镜像、`npm ci`、`vite build`。2 核 2G 的机器做这些事要 5 到 15 分钟，内存不够时还会被 OOM 杀掉（`exit code 137`）。

这套办法把编译搬到你的 Windows 电脑上：本机交叉编译出 linux/amd64 的二进制和前端静态文件，用 scp 传上去，服务器那一步只剩 `COPY`，十几秒就能出镜像。**你的电脑不需要装 Docker**，服务器也不需要多装任何东西。

分工是这样的：

| 在哪 | 做什么 | 花多久 |
|---|---|---|
| 你的电脑 | `go build` 加 `npm run build` | 1 到 3 分钟 |
| 传输 | scp 一个几十 MB 的压缩包 | 看上行带宽，通常十几秒 |
| 服务器 | `COPY` 进镜像、起容器 | 十几秒 |

下面用你给的地址 `root@47.86.29.94` 举例。以后换机器，把命令里的 IP 换掉即可。

#### 本机准备

需要 Go 1.26 以上与 Node 22，和 `Dockerfile`、CI 里用的版本对齐：

```bash
go version      # go1.27.x 也可以，go.mod 要求的是 1.26
node -v         # v22.x
```

`scp` 与 `ssh` 在 Windows 自带（Git Bash 里直接可用）。没有的话装 Git for Windows，或在「设置 → 应用 → 可选功能」里加 OpenSSH 客户端。

#### 服务器上只需要叠加层那一个文件

这套办法用到三个新文件，但只有第三个要单独出现在服务器上：

```
deploy/Dockerfile.artifact                后端运行镜像，只做 COPY
deploy/web.Dockerfile.artifact            前端运行镜像，只做 COPY
deploy/prod/docker-compose.artifact.yml   把构建上下文换成 .build 的叠加层
```

前两个在第一步打包时会被拷成 `.build/api/Dockerfile` 与 `.build/web/Dockerfile`，跟着 tar 包一起上去。叠加层里写的 `dockerfile: Dockerfile` 是相对 context（`.build/api`）解析的，所以服务器上真正被读到的就是包内那两份，`deploy/` 下有没有原文件都不影响。

只有叠加层要落在 `/opt/forum/deploy/prod/`。它还没进 git 的时候，服务器 `git clone` 下来的目录里没有，第四步 `docker compose config` 会报 `no such file or directory`。先从本机补传：

```bash
cd /d/go学习/forum-backend
scp deploy/prod/docker-compose.artifact.yml root@47.86.29.94:/opt/forum/deploy/prod/
```

核对：

```bash
ssh root@47.86.29.94 "ls -l /opt/forum/deploy/prod/docker-compose.artifact.yml; ls -l /opt/forum/.build/api/Dockerfile /opt/forum/.build/web/Dockerfile"
```

后面两条要等包解开才有，属于正常。

这个文件进了 git 之后，这一步就省了：服务器 `git pull` 就有了。判断方法是在本机 `git status --short deploy/prod/docker-compose.artifact.yml`，没有输出就说明已在版本里。

如果 `git clone` 的目录不是 `/opt/forum`，上面所有路径跟着换。

#### 第一步：本机打包

先提交，再打包。标签里带 commit 短哈希，工作区还有未提交改动时，包里的代码和这个哈希对不上，线上排查会被带偏、回退也找不到对应源码。脚本因此会直接拒绝：

```bash
cd /d/go学习/forum-backend
git status --short      # 确认要提交的就是这些
git add <改动的文件>
git commit -m "fix(web): ..."
git push
bash scripts/pack-linux.sh --tar
```

真的只想验证打包流程、不在乎标签准不准（比如第一次跑通脚本），加 `--allow-dirty` 放行。

`--tar` 表示额外打一个 `forum-linux-amd64.tar.gz`；不加只生成目录，适合你自己用 rsync 同步。

脚本做的每一件事都对应根目录 `Dockerfile` 里的一步，参数故意写成一样的，避免「本机编的和服务器编的行为不同」：

- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build`：在 Windows 上编出 Linux 的静态二进制。`CGO_ENABLED=0` 是关键，关掉 cgo 才不依赖 Linux 的 C 库，产物才能直接塞进 alpine 跑。
- `-trimpath -ldflags="-s -w"`：去掉本机绝对路径与符号表，体积小三成左右。
- `npm run build`：产出 `web/dist`。
- 把 `config/config.docker.yaml`、`deploy/nginx.docker.conf` 和两份 artifact 版 Dockerfile 一起放进产物目录。

产物结构：

```
.build/
  BUILD_INFO                 这次打包对应的标签、commit、时间、目标架构
  IMAGE_TAG                  只含一行标签，服务器上 $(cat .build/IMAGE_TAG) 直接用
  forum-linux-amd64.tar.gz   给服务器用的压缩包
  api/
    forum-server             后端
    forum-migrate            建表程序
    config/config.yaml       镜像内的默认配置
    Dockerfile               只做 COPY 的构建文件
  web/
    dist/                    前端静态文件
    default.conf             容器内 nginx 配置
    Dockerfile
```

`.build/` 已经在 `.gitignore` 与 `.dockerignore` 里：它是构建结果，不进仓库，普通的 `docker build` 也不会把它当上下文传一遍。

想确认包里有什么、对应哪一版代码：

```bash
tar -tzf .build/forum-linux-amd64.tar.gz | head -20
cat .build/BUILD_INFO
```

`BUILD_INFO` 的 `tag` 就是这次要用的镜像标签，形如 `1118-0228-7d2ac1f`：`月日-时分` 加 commit 短哈希。前半段说明什么时候打的包，后半段说明是哪个 commit——`docker images` 里看到哪个标签，就知道该 `git show` 哪次提交。`dirty` 正常应该是 0，不为 0 只可能出自 `--allow-dirty`。

#### 第二步：传到服务器

```bash
scp .build/forum-linux-amd64.tar.gz root@47.86.29.94:/tmp/
```

`scp` 的语法是 `scp 本地文件 用户@主机:目标路径`。第一次连接会问 `Are you sure you want to continue connecting?`，输 `yes`，然后输 root 密码。

用密钥登录就加 `-i`：

```bash
scp -i ~/.ssh/jiushixixi.pem .build/forum-linux-amd64.tar.gz root@47.86.29.94:/tmp/
```

`~/.ssh` 在 Windows 上是 `C:\Users\admin\.ssh`，Git Bash 里写 `~/.ssh` 就能找到。

SSH 端口不是 22 时用大写 `-P`：

```bash
scp -P 2222 .build/forum-linux-amd64.tar.gz root@47.86.29.94:/tmp/
```

注意 `scp` 的端口参数是大写 `-P`，`ssh` 的是小写 `-p`。这两个不一样，写混了会报 `unknown option`。

传到 `/tmp` 而不是直接传进 `/opt/forum`，是为了先看一眼包的结构再往项目目录里解，传错了不会污染项目。传完核对大小，避免网络中断留下半个包：

```bash
ssh root@47.86.29.94 "ls -lh /tmp/forum-linux-amd64.tar.gz"
```

与本机 `ls -lh .build/forum-linux-amd64.tar.gz` 看到的字节数一致就对了。

#### 第三步：服务器解包

```bash
ssh root@47.86.29.94
cd /opt/forum
tar -xzf /tmp/forum-linux-amd64.tar.gz -C /opt/forum
cat /opt/forum/.build/BUILD_INFO
```

`-C /opt/forum` 指定解到哪。包里存的路径就是 `.build/api`、`.build/web`，所以解完正好落在项目根下，不需要再挪文件，compose 里相对路径 `context: .build/api` 才成立。

解完顺手删掉压缩包，40G 系统盘别留垃圾：

```bash
rm -f /tmp/forum-linux-amd64.tar.gz
```

#### 第四步：改成三份 compose 文件

8.1 里设置的 `COMPOSE_FILE` 是两份，现在要加第三份，它负责把「构建来源」从源码换成 `.build` 里的产物：

```bash
export COMPOSE_FILE=/opt/forum/docker-compose.yml:/opt/forum/deploy/prod/docker-compose.prod.yml:/opt/forum/deploy/prod/docker-compose.artifact.yml
```

同样写进 `~/.bashrc` 长期生效：

```bash
echo 'export COMPOSE_FILE=/opt/forum/docker-compose.yml:/opt/forum/deploy/prod/docker-compose.prod.yml:/opt/forum/deploy/prod/docker-compose.artifact.yml' >> ~/.bashrc
source ~/.bashrc
```

顺序不能反，后面的文件覆盖前面的。第三份只改三件事：`api`、`migrate`、`web` 的 `build.context` 指到 `.build/api` 与 `.build/web`，镜像名固定成 `forum-api` 与 `forum-web`。端口、挂载、环境变量、依赖关系全部沿用前两份。

确认合并结果对：

```bash
docker compose config | grep -A2 'context:'
```

应该看到四处 `context`：两处是 `/opt/forum/.build/api`（api 与 migrate）、一处是 `/opt/forum/.build/web`，还有一处是 `/opt/forum/pool-sim`。如果前三个还是 `/opt/forum`，说明第三份没被读到，检查 `COMPOSE_FILE` 的路径。

`pool-sim` 这处**本来就该指向源码目录**，不是漏改：第三份叠加层只管 api、migrate、web 三个服务，因为只有它们能从「本机编译好的二进制、静态文件」里构建。Python 那套没有交叉编译这回事，镜像只能在服务器上装。

#### 第五步：打镜像

如果之前在服务器上试过源码构建（`docker compose build` 不带服务名，会把 `api`、`migrate`、`web`、`pool-sim` 四个服务一起编），先清掉那次留下的半截状态：

```bash
cd /opt/forum
docker compose down          # 停掉起了一半的容器；不要加 -v，数据卷要留
docker builder prune -f      # 清掉卡住的构建缓存，能腾出几百 MB 到几 G
df -h /
```

源码构建在 2 核 2G 上是「看起来卡住」而不是报错：`migrate` 和 `api` 共用 golang 镜像，要跑两遍 `go build`，内存不够时被内核 OOM 杀掉，`docker compose` 只会安静地停在那里。走本机编译之后，`golang` 与 `node` 这两个大镜像不会再被拉。

标签在第一步打包时就定好了，写在 `.build/IMAGE_TAG` 里跟着压缩包一起上来（形如 `1118-0228-7d2ac1f`）。这一步只是把它抄进 `.env`：

```bash
cd /opt/forum
sed -i '/^IMAGE_TAG=/d' .env
echo "IMAGE_TAG=$(cat .build/IMAGE_TAG)" >> .env
grep -n '^IMAGE_TAG=' .env
```

不要在服务器上自己用 `date +%m%d-%H%M` 算：那样算出来的是「上服务器的时间」，和本机打包时间差几分钟，而且不带 commit，`docker images` 里那一行对不回任何源码。

这里用「先删再追加」而不是 `sed "s/^IMAGE_TAG=.*/…/"`：`.env` 里本来没有这一行时，`s///` 匹配不到任何东西，命令返回 0、什么也不做，而且不会有任何提示。标签于是一直落到默认值 `local`，`docker images` 里全是同名镜像，回退时无版可选。早先复制出来的 `.env` 里往往只有一行注释 `# IMAGE_TAG=local`，正好命中这种情况。

`IMAGE_TAG` 是 `docker-compose.artifact.yml` 里 `${IMAGE_TAG:-local}` 取的值。写进 `.env` 而不是 `export`，是因为 compose 会自动读项目目录下的 `.env`，你下次开一个新终端它还在，`build` 与后面的 `up -d` 一定用的是同一个标签。

用 `export` 的后果很具体：关掉终端就没了，之后 `up -d` 会去找默认的 `local`，启动的可能是另一份旧镜像。

每次都换标签的理由：不换的话所有版本都叫 `local`，出问题想回上一版就无从下手。现在的标签自带 commit 哈希，`docker images` 里的每一行都能直接对到某次提交，回退时也清楚退的是哪一版代码。

然后构建：

```bash
docker compose build api web
```

这次只做三件事：拉 `alpine:3.20` 与 `nginx:1.27-alpine`（第一次要下几十 MB）、`apk add ca-certificates tzdata`、把产物 `COPY` 进去。没有编译，2 核 2G 也能在一分钟内完成。

`api` 与 `migrate` 用的是同一个镜像，`build api` 之后 `migrate` 不用再单独构建。

台球的物理服务不在这套产物里，服务器上单独构建一次（第一次两三分钟，之后只有 `pool-sim/` 改动才重建）：

```bash
docker compose build pool-sim
```

别漏这一步。后面第六步用的是 `up -d --no-build`，镜像不存在时它不会替你构建，而是去尝试 pull 一个根本不存在的 `forum_pool-sim`，报 `pull access denied`。

#### 第六步：起服务

首次部署按 8.3 的顺序：

```bash
docker compose up -d mysql redis
docker compose run --rm migrate
docker compose up -d --no-build
```

`--no-build` 是把意图写清楚：镜像已经构建好了，不要因为看到 build 定义又回去编译。镜像存在时它本来就不会编译，这个参数是防止哪天改乱了文件顺序又走回编译路径。

`docker compose run --rm migrate` 用的就是刚构建的 `forum-api:<IMAGE_TAG>`，和 api 完全同一份代码。

`up -d` 会顺带拉起 `pool-sim`，它的 `start_period` 是 40 秒，头半分钟显示 `health: starting` 属正常。`api` 只等它「启动」不等它「健康」：那时出杆会拿到一句「球桌暂时算不动」，重试一次就好，而把 api 一起拖着不启动会连文章都看不了。

探活：

```bash
docker compose ps
# 此时仍是 nginx.http.conf，80 端口直服，用 http 探活
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1/readyz
```

#### 以后发版

只改了后端：

```bash
# 本机：先提交，脚本会拦下未提交的改动
cd /d/go学习/forum-backend
git add <改动的文件> && git commit -m "fix(api): ..." && git push
bash scripts/pack-linux.sh --api-only --tar
scp .build/forum-linux-amd64.tar.gz root@47.86.29.94:/tmp/

# 服务器
cd /opt/forum
tar -xzf /tmp/forum-linux-amd64.tar.gz -C /opt/forum
sed -i '/^IMAGE_TAG=/d' .env && echo "IMAGE_TAG=$(cat .build/IMAGE_TAG)" >> .env
rm -f /tmp/forum-linux-amd64.tar.gz
docker compose build api web
docker compose up -d --no-deps api
docker compose up -d --no-deps --force-recreate web
```

`--api-only` 让脚本跳过 `npm run build`，几十秒就能打包完。

最后那条重建 web 不能省，理由在 9.2 与 13.1：Nginx 启动时把 `api` 解析成 IP 缓存住，api 容器换了 IP 它不知道，继续往旧 IP 发请求，结果是全站 502。重建一次让它重新解析，一两秒的事。

只改了前端：

```bash
# 本机
git add <改动的文件> && git commit -m "fix(web): ..." && git push
bash scripts/pack-linux.sh --web-only --tar
scp .build/forum-linux-amd64.tar.gz root@47.86.29.94:/tmp/

# 服务器
cd /opt/forum
tar -xzf /tmp/forum-linux-amd64.tar.gz -C /opt/forum
sed -i '/^IMAGE_TAG=/d' .env && echo "IMAGE_TAG=$(cat .build/IMAGE_TAG)" >> .env
rm -f /tmp/forum-linux-amd64.tar.gz
docker compose build api web
docker compose up -d --no-deps --force-recreate web
```

改了表结构的话，在 `up -d api` 之前加一步 `docker compose run --rm migrate`。

只改了台球的物理服务（`pool-sim/` 下的 Python 代码或 `requirements.txt`）：

```bash
# 本机：只提交，不用打包，也不用 scp
git add pool-sim && git commit -m "fix(pool-sim): ..." && git push

# 服务器：源码本来就在，直接拉下来重建
cd /opt/forum
git pull --ff-only
docker compose build pool-sim
docker compose up -d --no-deps pool-sim
```

这条路径**不经过 `.build`**，也就跟 `IMAGE_TAG` 无关：tag 只管 api 与 web 那两个镜像，`pool-sim` 的镜像名是 compose 默认生成的 `forum-pool-sim`，没有版本后缀。所以改了 `pool-sim` 之后 `docker images` 里不会多出一行，别以为没生效——看 `docker compose ps pool-sim` 的 `CREATED` 时间。

`--no-deps` 不能省。`pool-sim` 是 api 的依赖，不加这个参数 compose 会认为「依赖变了」，把 api 一起重启，进行中的对局全断。它是无状态的（每次请求都从给定球位重算），换容器之后 api 的下一次出杆自然连到新的，对局不受影响。

两个发版块里都是 `build api web`，即使只改了一端。原因是标签是全局的：只构建 api 的话，`IMAGE_TAG` 换了而 web 镜像还停在旧标签，下一次 `up -d` 会在服务器上临时补一次构建，回退时也会发现某个标签缺一个镜像。两端一起构建，`docker images` 里每个标签都是成对的，代价只是多十几秒 COPY。

#### 回退到上一版

```bash
docker images | grep forum-api
```

每一行是某一次构建：

```
forum-api   1118-1012-7d2ac1f   a1b2c3d4e5f6   2 minutes ago   62MB
forum-api   1117-2310-3f91ab2   9f8e7d6c5b4a   3 hours ago     62MB
```

标签后半段就是 commit，`git show 3f91ab2` 能直接看到那一版改了什么，不用靠时间戳猜。

把 `.env` 里的标签改回旧值，再起容器：

```bash
sed -i '/^IMAGE_TAG=/d' .env && echo "IMAGE_TAG=1117-2310-3f91ab2" >> .env
docker compose up -d --no-deps api
docker compose up -d --no-deps --force-recreate web
```

镜像还在本地，不用重新构建，几秒就切回去。这也是每次换标签最实际的好处。

#### 服务器上要不要留源码

不需要，**只有一处例外：`pool-sim/`**。Go 与前端那两坨整条链路里服务器做的只有 `COPY`，`cmd/`、`internal/`、`pkg/`、`web/src/`、根 `Dockerfile`、`deploy/web.Dockerfile` 一次都不会被读到。但台球的物理服务是 Python，没有交叉编译这回事，它的构建上下文就是 `pool-sim/` 目录本身——删了它，`docker compose build pool-sim` 会报 `Cannot locate specified Dockerfile`，台球直接不能玩。

运行时真正用得上的只有这几类：

| 文件 | 用途 |
|---|---|
| `docker-compose.yml` + `deploy/prod/docker-compose.prod.yml` + `deploy/prod/docker-compose.artifact.yml` | 除了端口和变量，剩下的就是「用哪份产物建镜像」 |
| `.build/` | 解包得到的二进制、`dist`，以及两处 `Dockerfile`（包里自带） |
| `pool-sim/` | 唯一还要在服务器上编译的东西，`Dockerfile`、`app/`、`requirements.txt` 都不能少 |
| `.env`、`config/config.yaml` | 密码、密钥、站点配置 |
| `deploy/prod/nginx.conf`、`locations.conf`、`my.cnf` | 挂载进容器 |
| `scripts/backup.sh`、`certbot/`、`uploads/` | 备份、证书、用户上传 |

但也不建议直接 `rm -rf`：`git pull` 是更新 compose 与 `deploy/prod/` 下那些配置的唯一手段，删了就只能一个个 `scp`，迟早漏一个文件。

折中的做法是把工作区瘦成「只留部署相关文件」，用 git 自带的 sparse-checkout：

```bash
cd /opt/forum
git sparse-checkout init --no-cone
git sparse-checkout set '/docker-compose.yml' '/deploy/' '/pool-sim/' '/scripts/' '/docs/' '/README.md' '/.env.example'
ls
```

`'/pool-sim/'` 这一条不能少，理由见上：它是唯一还要在服务器上编译的东西。少了它，`git pull` 拉下来的工作区里没有这个目录，`docker compose build pool-sim` 立刻失败。

它只改工作区，`.git` 里的历史不动，`git pull` 照常。`.build/`、`certbot/`、`uploads/`、`.env`、`config/config.yaml` 都是未跟踪或被忽略的文件，不受影响。

好处不只是省几十兆磁盘，更重要的是堵住了最危险的那条路：没有 Go 源码时，一旦 `COMPOSE_FILE` 丢了、`context` 退回 `.`，`docker compose build` 会**立刻**报 `Cannot locate specified Dockerfile`，而不是像现在这样安静地开始编译、把 2G 内存吃穿、连 ssh 都登不上。

不过留着 `pool-sim/` 之后，这条护栏只剩一半：不带服务名的 `docker compose build` 仍然会真的开始装 Python 依赖。所以发版时永远写清服务名（`build api web` 或 `build pool-sim`），不要图省事留空。

要恢复源码：`git sparse-checkout disable`。

#### 这套方式的坑

- **架构必须对**。脚本里写死 `GOARCH=amd64`，阿里云经济型 e 是 x86，正确。如果换成 ARM 实例（倚天 710），要改成 `GOARCH=arm64`，否则容器起来报 `exec format error`。
- **忘了 build**。`up -d` 发现同名镜像已经存在就不会重建，你会以为「代码传上去了但没生效」。每次先 `build` 再 `up`。
- **`.build` 和 git 不是一回事**。换一台电脑、删掉 `.build`、或者 `git pull` 之后都要重新打包。`BUILD_INFO` 里的 commit 与 dirty 就是用来核对这个的。
- **镜像标签一直是 `local`**。`local` 是 `${IMAGE_TAG:-local}` 的兜底值，不是自动生成的。三种原因：`.env` 里没有 `IMAGE_TAG=` 这一行（用 `sed s///` 改不存在的行会静默失败）、改了 `.env` 却没有重新 `build`（标签是构建时定下的名字，不会追溯改名）、`.build/IMAGE_TAG` 没跟包一起上来（旧包或解错了位置）。查当前解析结果：`docker compose config | grep 'image:'`，两处 `forum-api:` + 一处 `forum-web:` 的那个值就是下次 `build` 会打的标签。
- **服务器上不能临时走源码编译**。只要 `COMPOSE_FILE` 带着第三份文件，context 就指向 `.build`，没传产物会报 `no such file or directory`。真想临时走源码编译，把第三份从 `COMPOSE_FILE` 里去掉再 `docker compose build`。
- **uploads 目录权限照旧**。8.2 那条 `chown -R 10001:10001 uploads` 与编译方式无关，仍然要做。
- **`pool-sim` 不在这套产物里，也不跟 `IMAGE_TAG` 走**。它要单独 `build pool-sim`，源码也得留在服务器上。更要紧的是回退：改 `.env` 里的 `IMAGE_TAG` 能把 api 与 web 带回旧版，**带不回旧的物理服务**——它没有版本标签，永远是「最后一次构建出来的那个」。真改过 `pool-sim` 又想回退，只能 `git checkout <旧提交> -- pool-sim/ && docker compose build pool-sim && docker compose up -d --no-deps pool-sim`。
- **本机 Go 版本不能低于 go.mod 要求**。`go.mod` 写的是 `go 1.26`，本机 1.27 可以，1.25 会直接报错。

### 13.6 台球：物理服务与房间

台球比别人多两样东西要照看：一个 Python 容器 `pool-sim`，和一堆「点进页面就存在、没人来就该消失」的房间。

#### pool-sim 活着没有

```bash
docker compose ps pool-sim
docker inspect --format '{{.State.Health.Status}}' "$(docker compose ps -q pool-sim)"
```

第二条要打印 `healthy`。`starting` 是还在做 JIT 预热（启动后先跑一杆满台开球，约五秒），持续超过 40 秒就是真的有问题，看日志：

```bash
docker compose logs --tail=100 pool-sim
```

`/healthz` 只要进程能应答就返回 200，它**不证明会算球**。真正出问题时的痕迹在 api 侧：

```bash
docker compose logs --tail=200 api | grep -i simulat
```

看到 `simulate shot` 且带 `error`，说明 api 连不上它或者它算挂了；用户那边看到的是「球桌暂时算不动，稍后再试」。

它被 OOM 杀掉过一次的话，`dmesg` 里留得住证据：

```bash
dmesg | grep -i -E 'oom|killed process' | tail
```

compose 给它限了 768M。满力开球那一杆 numba 的临时数组最占内存，512M 会在那时被杀。真要调小，先开一局满力开球，同时 `docker stats pool-sim` 看峰值。

#### 房间与对局的回收

台球有两条回收线，管的是两件不同的事，缺一条就会留下垃圾。

**内存里那条**：`Manager.Janitor` 每分钟扫一遍进程内的房间，收掉「已结束超过十分钟」的和「还没开局、并且十分钟没人跟它说过话」的。后一条是「我的房间」上线之后加的——点进 `/pool` 就开一间房，不收拾的话一个人一天能攒几十个 goroutine。日志是 Debug 级：

```
reap idle pool match  match_id=1234
```

默认 `config.yaml` 里 `log.level: info`，所以平时看不到。要盯这条，把它改成 `debug` 再 `docker compose restart api`，代价是日志量大好几倍，看完记得改回来。

**数据库里那条**：`pool.ExpireRooms` 每分钟把 `status='waiting'` 且建了超过两小时的房间标成 `expired`，日志是 Info 级，条数为 0 时**不打**（避免每分钟一行噪音）：

```
expired stale pool rooms  count=3
```

两条都要的原因很直接：内存那条管 goroutine，但它随进程一起没；进程重启（发版、OOM、崩溃）之后，库里那些 `waiting` 的行没人认领了，只能靠定时扫把它们收掉。

查当前状态：

```bash
docker compose exec -T mysql sh -c 'mysql -uroot -p"$MYSQL_ROOT_PASSWORD" forum -e "SELECT status, COUNT(*) AS n FROM pool_matches GROUP BY status"'
```

注意这条命令里的变量是在**容器里**展开的（`sh -c '...'` 用单引号包住），宿主机上并没有 `MYSQL_ROOT_PASSWORD` 这个变量。

只应该有 `waiting` / `playing` / `finished` / `expired` 四种。`waiting` 的行数应当跟「此刻真在大厅里等人的人数」差不多，一直涨说明清扫没跑起来。

看有没有该被收掉却没被收的：

```bash
docker compose exec -T mysql sh -c 'mysql -uroot -p"$MYSQL_ROOT_PASSWORD" forum -e "SELECT COUNT(*) FROM pool_matches WHERE status=\"waiting\" AND created_at < NOW() - INTERVAL 2 HOUR"'
```

这条应当恒为 0。不是 0 就 `docker compose logs api | grep 'expired stale'`，看协程是不是压根没起（启动日志里会有它的启停记录）。

最近开过的几间房，出问题时报给开发看这个：

```bash
docker compose exec -T mysql sh -c 'mysql -uroot -p"$MYSQL_ROOT_PASSWORD" forum -e "SELECT id, room_code, p0_id, p1_id, status, created_at FROM pool_matches ORDER BY id DESC LIMIT 20"'
```

`p1_id` 为 `NULL` 就是对面还空着。房号是六位数字，一共一百万种，撞了靠唯一索引兜住、建房时重试五次，不用管。

#### 升级时唯一要留意的一列

台球这一轮把 `pool_matches.p1_id` 从「必填」改成了「可为空」——房间建好时可以只有房主。走 `docker compose run --rm migrate` 时 GORM 会比对这一列的可空性并尝试改，但这是**改已有表的列**，比建新表更容易被版本差异影响，升降级前建议手动确认一次：

```bash
docker compose exec -T mysql sh -c 'mysql -uroot -p"$MYSQL_ROOT_PASSWORD" forum -e "SHOW COLUMNS FROM pool_matches LIKE \"p1_id\""'
```

`Null` 那列要是 `NO`，手动改：

```sql
ALTER TABLE pool_matches MODIFY p1_id BIGINT UNSIGNED NULL;
```

检查无误再 `up -d --no-deps api`：新代码会给这一列写 `NULL`，列上还是 `NOT NULL` 的话，一开房就是 500（`Column 'p1_id' cannot be null`），而且只在**没被邀请、自己开房**这条路上出现，邀请那条因为直接带上了对方 id 反而是好的——排查时很容易被这个差异带偏。

#### 用户报「打不了」时按这个顺序看

| 用户看到 | 先查 | 多半是 |
|---|---|---|
| 「球桌暂时算不动」 | `docker compose ps pool-sim`、`logs pool-sim` | 容器在 `starting`（等十秒）或已经挂了 |
| 输房号提示「房间不存在或已满」 | 上一条 SQL，看那间房的 `status` 与 `p1_id` | 房被两小时规则收掉了，或对面已经坐人 |
| 进大厅立刻被踢回登录 | `logs api \| grep 401` | 登录态过期，与台球无关 |
| 一直显示「你正在对局中」 | `SELECT * FROM pool_matches WHERE status='playing'` | 进程重启过，那局已经没了但库里还挂着；等它结算或手动改 `finished` |
| 两边都点了准备却进不去 | `logs api \| grep 'mark pool match playing'` | 改状态那步失败，通常伴随数据库报错 |

---

## 14. 2G 机器的三条护栏

### 14.1 Go 的内存上限

`deploy/prod/docker-compose.prod.yml` 里给 api 设了 `GOMEMLIMIT: 256MiB`。这个变量是 Go 1.19 引入的软上限：堆接近这个值时 GC 会更积极地回收，而不是继续向系统要内存。设了它，即使有内存泄漏，进程也会先变慢而不是把整台机器拖死。

### 14.2 MySQL 的参数

`deploy/prod/my.cnf` 做了三件事：把 buffer pool 定在 128M、关掉 performance_schema、把连接数限制到 50。这三项合计能把 MySQL 的常驻内存从 500MB 压到 300MB 左右。具体每条的理由写在文件注释里。

如果以后要做性能分析，把 `performance_schema = OFF` 改回 `ON` 再重启 mysql，代价是多占一两百 MB。

### 14.3 构建时的内存

四条路，按推荐顺序：

1. **本机编译、服务器只打镜像**（13.5）。编译这件事完全离开服务器，`go build` 与 `npm run build` 都在你自己的电脑上跑，服务器那一步只剩 `COPY`，根本碰不到内存峰值。不用装 Docker，也不用镜像仓库，代价只是多一条 scp。
2. **加 swap**（第 5.3 节）。最简单，代价是构建慢一点。已经按 13.5 部署的话，swap 仍然留着，它兜的是运行期而不是构建期。
3. **先单独构建前端**，再构建后端。两个峰值不在同一时间出现：
   ```bash
   docker compose build web
   docker compose build api
   ```
4. **在本地构建好镜像推到镜像仓库**。需要阿里云容器镜像服务（ACR）或个人版免费额度，流程是本地 `docker build` → `docker push` → 服务器 `docker pull` → `up -d`。比 13.5 多一步「本机装 Docker」，好处是连基础镜像都不用服务器下载。只有 13.5 也不够用时才值得上这套。

---

## 15. 排错速查

| 现象 | 最可能的原因 | 怎么查、怎么修 |
|---|---|---|
| 502 Bad Gateway | api 没起来，或 Nginx 缓存的 IP 过期 | `docker compose logs api` 看有没有报错；`docker compose up -d --no-deps --force-recreate web` |
| 504 Gateway Timeout | 后端慢，通常是数据库锁或慢查询 | `docker compose logs api \| grep slow`；`docker compose exec mysql mysql -e "SHOW PROCESSLIST"` |
| 站点一直转圈，无响应 | 磁盘满或内存被打穿 | `df -h`、`free -h`、`dmesg \| tail -30` 看 OOM 记录 |
| `curl http://` 报 400 | 用 http 访问了 443 端口（或反过来） | 看清楚协议和端口 |
| 证书申请失败 `Timeout` | DNS 没生效 / 安全组没开 80 / Nginx 没起 | `dig +short 域名`；安全组看 80；`docker compose ps` |
| 证书申请失败 404 | 验证路径的 root 或挂载不对 | 对照 9.1 与 10.2 的路径 |
| WebSocket 连不上，403 | `ws.allowed_origins` 没加域名 | 改 `config/config.yaml`，`docker compose restart api` |
| WebSocket 连不上，426/400 | Nginx 没转发 Upgrade 头 | 检查 9.3 那四行是否齐全 |
| 页面时间差 8 小时 | 容器时区是 UTC | 检查 prod compose 里的 `TZ`，`docker compose exec api date` |
| 登录后立刻掉线 | Redis 没起来或密码不对 | `docker compose logs api` 找 redis 相关报错；`docker compose ps redis` |
| 注册报 500，日志 `Access denied` | `.env` 的密码与数据卷里的不一致 | 见下一条 |
| 改了 `.env` 密码但连不上 | mysql 容器只在**首次**初始化时读密码，数据卷已存在就跳过 | 要么改回旧密码，要么进容器 `ALTER USER` 改密码 |
| 构建报 `signal: killed` / 137 | 内存不足 | 加 swap；分开构建 api 和 web |
| `unknown tag !override` | Compose 版本低于 2.24.4 | `apt -y install docker-compose-plugin` 升级 |
| `no configuration file provided` | `COMPOSE_FILE` 用了相对路径，当前目录不对 | 改成绝对路径（8.1） |
| 挂载配置文件后容器起不来，日志说是个目录 | 宿主机上那个文件不存在，Docker 自动建了同名目录 | `rm -rf` 那个目录，先 `cp` 出文件再 `up -d` |
| 上传头像 413 | 超过 Nginx 的 `client_max_body_size 1m` | 这是有意限制，换小图；要放宽就改 `locations.conf` 和后端的 `limitBody` |
| 前端刷新 404 | SPA 回退规则丢了 | 检查 `location /` 的 `try_files`（9.5） |
| 台球出杆提示「球桌暂时算不动」 | `pool-sim` 没起、还在预热，或者挂了 | `docker compose ps pool-sim`、`docker compose logs pool-sim`，见 13.6 |
| `up -d` 报 `pull access denied ... pool-sim` | 只构建了 api 与 web，`--no-build` 找不到 `pool-sim` 镜像 | `docker compose build pool-sim` 再 `up -d --no-build` |
| 开房报 500，日志 `Column 'p1_id' cannot be null` | 列还是 `NOT NULL`（改表没生效） | 按 13.6 那条 `ALTER TABLE ... MODIFY p1_id ... NULL` |
| 大厅房号跳来跳去、对面进不来 | 库里同一人有多间 `waiting`，或房被两小时规则收掉 | 按 13.6 的 SQL 查 `waiting` 行；看 `expired stale pool rooms` 日志 |
| 台球对局 WS 握手 426 | `/api/ws/game` 落进了普通 `location /api/`，少了 Upgrade 头 | 检查 `locations.conf` 里 `/api/ws/` 那段（12.15 第 3 条） |

---

## 16. 三个月之后

试用到期后有三个选择：

1. **继续用这台，按量付费**。2 核 2G 约 0.167 元/小时，一个月 120 元左右，再加带宽。适合「还在用」的情况。
2. **换成包年包月**。长期用比按量便宜三到五折，而且包年包月是备案的前提（如果以后搬大陆）。
3. **换轻量应用服务器**。轻量的价格通常包含带宽和流量包，对「只跑一个站」更划算，缺点是可调参数少、不能随意换配置。下单前用它和 ECS 的月付价对比一下。

迁机器时，要带走的是三样东西：

- `backups/` 里最新的数据库 dump
- `config/config.yaml` 和 `.env`（两个都含密钥）
- `certbot/conf`（证书和私钥，可以省一次签发）

新机器上按第 5 到 8 节重做一遍，把 dump 导进去，最后改 DNS 的 A 记录指向新 IP。**TTL 只有 600 秒，改完 10 分钟内就会切过去**，旧机器可以再留一天确认没问题再释放。

---

## 附：命令速查

从零到上线，按顺序抄：

```bash
# 1. 系统初始化
apt update && apt -y upgrade
apt -y install git curl ca-certificates
timedatectl set-timezone Asia/Shanghai
fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile
echo '/swapfile none swap sw 0 0' >> /etc/fstab

# 2. 装 Docker
curl -fsSL https://get.docker.com -o /tmp/get-docker.sh && sh /tmp/get-docker.sh
docker compose version     # 需要 >= 2.24.4

# 3. 拉代码（先按 6.2-6.4 配好 deploy key）
mkdir -p /opt/forum
git clone git@github.com:<你的用户名>/<仓库名>.git /opt/forum
cd /opt/forum

# 4. 配置
cp .env.example .env
cp config/config.docker.yaml config/config.yaml
openssl rand -hex 16    # 填进 .env 的 MYSQL_PASSWORD
openssl rand -hex 32    # 填进 .env 的 JWT_SECRET
vi .env                 # 填密码，NGINX_CONF 先写 nginx.http.conf
vi config/config.yaml   # 改 ws.allowed_origins 与 admin.user_ids
chmod 600 .env
chown 10001:10001 config/config.yaml && chmod 640 config/config.yaml   # 容器内以 uid 10001 读它

# 5. 构建与启动
#    按 13.5 走：先在【本机】执行 bash scripts/pack-linux.sh --tar
#    再 scp .build/forum-linux-amd64.tar.gz root@47.86.29.94:/tmp/
#    登录服务器后 tar -xzf /tmp/forum-linux-amd64.tar.gz -C /opt/forum
#    （不走 13.5 的话，COMPOSE_FILE 去掉最后一段，build api web 换成 build）
export COMPOSE_FILE=/opt/forum/docker-compose.yml:/opt/forum/deploy/prod/docker-compose.prod.yml:/opt/forum/deploy/prod/docker-compose.artifact.yml
docker compose config | head -40
docker compose build api web
docker compose build pool-sim                     # Python，服务器上单独构建一次，见 13.6
docker compose up -d mysql redis
docker compose run --rm migrate
docker compose up -d --no-build
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1/readyz   # 证书签发前，80 直服

# 6. 域名解析生效后申请证书
mkdir -p certbot/www certbot/conf
docker run --rm -v /opt/forum/certbot/conf:/etc/letsencrypt -v /opt/forum/certbot/www:/var/www/certbot \
  certbot/certbot certonly --webroot -w /var/www/certbot \
  -d jiushixixi.xyz -d www.jiushixixi.xyz \
  --email 你的邮箱@example.com --agree-tos --no-eff-email

# 7. 切到 HTTPS
sed -i 's/^NGINX_CONF=.*/NGINX_CONF=nginx.conf/' .env
docker compose up -d --no-deps --force-recreate web
curl -I https://jiushixixi.xyz

# 8. 自动续期（crontab -e 加一行，见 10.4）
```

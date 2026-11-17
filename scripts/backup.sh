#!/usr/bin/env bash
# 数据库备份：mysqldump 到 backups/，按天数保留，自带一次完整性校验。
# 同时把 uploads/（用户上传的头像）打成 tar.gz：那是用户数据，
# 丢了没法从库里重建，虽然不值钱，但重来一遍要挨个通知用户重新上传。
#
# 为什么只备 MySQL，不备 Redis：
#   Redis 里除了 refresh token 的登记表，其余全是可重建的缓存与计数。
#   丢登记表的代价是「所有人重新登录一次」，比丢文章轻得多；
#   而 RDB 每份都是全量快照，跟着天天存会让备份体积翻倍。
#
# 用法：
#   scripts/backup.sh                  # 用下面的默认值
#   DB_HOST=10.0.0.5 scripts/backup.sh # 连远程库
#   KEEP_DAYS=30 scripts/backup.sh     # 保留 30 天
#   BACKUP_MODE=docker scripts/backup.sh  # 从 compose 的 mysql 容器里导
#
# 建议放进 crontab，每天凌晨跑一次：
#   0 3 * * * cd /srv/forum && scripts/backup.sh >> logs/backup.log 2>&1
set -euo pipefail

# 所有参数都能用环境变量覆盖，方便在 crontab 里直接改，不用编辑脚本。
DB_HOST="${DB_HOST:-127.0.0.1}"
DB_PORT="${DB_PORT:-3306}"
DB_USER="${DB_USER:-root}"
DB_PASSWORD="${DB_PASSWORD:-123456}"
DB_NAME="${DB_NAME:-forum}"
KEEP_DAYS="${KEEP_DAYS:-7}"
BACKUP_MODE="${BACKUP_MODE:-local}"
COMPOSE_SERVICE="${COMPOSE_SERVICE:-mysql}"

# 脚本可能被 crontab 以相对路径调用，先切到自己所在仓库的根目录，
# 否则 backups/ 会落在 crontab 的 $HOME 下，找的时候一头雾水。
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"
BACKUP_DIR="${BACKUP_DIR:-$REPO_DIR/backups}"
mkdir -p "$BACKUP_DIR"

STAMP="$(date +%Y%m%d-%H%M%S)"
TARGET="$BACKUP_DIR/forum-$STAMP.sql.gz"

# --single-transaction：InnoDB 下开一个一致性快照，不锁表。
# 不加这一条，备份期间所有写入都会被 InnoDB 的行锁挡住，用户会看到发布卡住。
# --no-tablespaces：MySQL 8 里普通用户没有 PROCESS 权限时，
# 导出表空间信息会直接报错退出，加这个参数就不查了。
DUMP_ARGS=(
  --single-transaction
  --no-tablespaces
  --default-character-set=utf8mb4
  --skip-add-locks
  --routines
  --triggers
  "$DB_NAME"
)

echo "[$(date '+%F %T')] 开始备份 $DB_NAME -> $TARGET"

if [ "$BACKUP_MODE" = "docker" ]; then
  # 容器里没有宿主机的时间戳目录，直接用 stdout 接到 gzip。
  # -e 把密码传给容器内的 mysqldump，避免密码出现在宿主机进程列表里。
  docker compose exec -T -e MYSQL_PWD="$DB_PASSWORD" "$COMPOSE_SERVICE" \
    mysqldump -u"$DB_USER" "${DUMP_ARGS[@]}" | gzip -6 > "$TARGET"
else
  # MYSQL_PWD 而不是 -p：命令行上的密码会被 ps 看到。
  MYSQL_PWD="$DB_PASSWORD" mysqldump -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" \
    "${DUMP_ARGS[@]}" | gzip -6 > "$TARGET"
fi

# 校验一：文件要能解压且不是空的。
# 只看退出码不够，mysqldump 失败时管道里的 gzip 仍然会「成功」写一个空文件。
if ! gzip -t "$TARGET" 2>/dev/null; then
  echo "[$(date '+%F %T')] 备份损坏，删除 $TARGET" >&2
  rm -f "$TARGET"
  exit 1
fi
SIZE="$(du -h "$TARGET" | cut -f1)"
if [ "$(stat -c%s "$TARGET" 2>/dev/null || stat -f%z "$TARGET")" -lt 1024 ]; then
  echo "[$(date '+%F %T')] 备份小于 1KB，基本可以确定是空的，删除 $TARGET" >&2
  rm -f "$TARGET"
  exit 1
fi

# 校验二：解压后要能看到建表语句。这一步才能发现「连上了但没选对库」这类问题：
# 那种情况下 dump 只有头部注释，gzip 校验照样通过。
if ! gzip -dc "$TARGET" | grep -q "CREATE TABLE"; then
  echo "[$(date '+%F %T')] 备份里没有建表语句，可能连错了库，删除 $TARGET" >&2
  rm -f "$TARGET"
  exit 1
fi

echo "[$(date '+%F %T')] 备份完成 $TARGET（$SIZE）"

# 上传目录单独打包。放在数据库备份成功之后：库才是主体，
# 头像备份失败不该把整个备份任务判成失败（那会让 cron 天天报错，
# 而报错久了就没人看了）。目录不存在时直接跳过，首次部署还没人上传过就是这种情况。
UPLOAD_DIR="${UPLOAD_DIR:-$REPO_DIR/uploads}"
if [ -d "$UPLOAD_DIR" ]; then
  UPLOAD_TARGET="$BACKUP_DIR/uploads-$STAMP.tar.gz"
  # -C 先切到父目录再打包，解压时不会带上一串绝对路径。
  if tar -czf "$UPLOAD_TARGET" -C "$(dirname "$UPLOAD_DIR")" "$(basename "$UPLOAD_DIR")"; then
    echo "[$(date '+%F %T')] 上传目录已备份 $UPLOAD_TARGET（$(du -h "$UPLOAD_TARGET" | cut -f1)）"
  else
    echo "[$(date '+%F %T')] 上传目录备份失败，数据库备份仍然有效" >&2
  fi
fi

# 清理过期备份。只在备份成功后执行：备份天天失败还删旧的，等于把退路删光。
DELETED="$(find "$BACKUP_DIR" \( -name 'forum-*.sql.gz' -o -name 'uploads-*.tar.gz' \) -type f -mtime +"$KEEP_DAYS" -print -delete | wc -l)"
echo "[$(date '+%F %T')] 清理 $KEEP_DAYS 天前的备份，共 $DELETED 份"

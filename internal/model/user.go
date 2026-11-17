// Package model 定义 GORM 数据模型。
package model

import "time"

// User 用户表。
// Username 是登录名，唯一索引兜住并发注册同名；Password 存 bcrypt 哈希，
// json:"-" 保证任何直接序列化 model 的路径都不会把哈希漏出去。
//
// Nickname / AvatarURL / Bio 是展示资料，与登录凭证解耦：
// 改昵称不影响登录，也不会撞唯一索引。
// 三列都是 not null + default ”，存量数据迁移时不用回填 NULL；
// 昵称为空时展示层回退到用户名，不在 SQL 里写 COALESCE。
type User struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	Username  string `gorm:"size:64;uniqueIndex;not null" json:"username"`
	Nickname  string `gorm:"size:32;not null;default:''" json:"nickname"`
	AvatarURL string `gorm:"size:255;not null;default:''" json:"avatar_url"`
	Bio       string `gorm:"size:200;not null;default:''" json:"bio"`
	Password  string `gorm:"size:128;not null" json:"-"` // bcrypt 哈希，绝不返回明文
	// BannedUntil 是封禁解除时间，NULL 表示没被封过或早已解除。
	//
	// 用指针而不是 time.Time：零值 time 与「封禁到公元 1 年」在类型上无法区分，
	// 判断就得写 `!IsZero() && After(now)` 这种绕的表达式，还容易漏掉一半。
	// NULL 到位就是「没有封禁」，判断只有一个 `!= nil && After(now)`。
	//
	// 解封不需要定时任务：每次判断都拿当前时间比一次，到期自然失效，
	// 库里那行旧值留着不碍事，也顺便成了「这人曾经被封过」的记录。
	BannedUntil *time.Time `json:"banned_until,omitempty"`
	// BanReason 只给管理员看，对外一律不返回：写清「因恶意刷屏封 7 天」这类内部判断，
	// 暴露给本人只会引发争论，而处置依据应该在举报记录里查。
	BanReason string    `gorm:"size:200;not null;default:''" json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

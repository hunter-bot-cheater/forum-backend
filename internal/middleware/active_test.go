package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// fakeBan 是固定封禁状态的假实现。
// 中间件只依赖「查一次封禁时间」这一个能力，用假实现能把用例限制在中间件本身，
// 不必为了测一个 403 去连数据库。
type fakeBan struct {
	until time.Time
}

func (f fakeBan) BannedUntil(context.Context, uint) (time.Time, error) {
	return f.until, nil
}

// serveWithActive 用真实中间件链跑一次请求，返回状态码、解析后的错误码与 handler 是否被调用。
// 先 setUserID 再挂中间件，复现「JWTAuth 已经写入 userID」的前置条件。
func serveWithActive(t *testing.T, checker BanChecker) (code int, errorCode string, message string, reached bool) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(userIDKey, uint(7))
		c.Next()
	})
	r.Use(RequireActive(checker))
	r.POST("/articles", func(c *gin.Context) {
		reached = true
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/articles", strings.NewReader("{}"))
	r.ServeHTTP(w, req)

	var body struct {
		Message   string `json:"message"`
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v，原文 %s", err, w.Body.String())
	}
	return w.Code, body.ErrorCode, body.Message, reached
}

// TestRequireActiveLetsNormalUserThrough 验证没被封禁的用户照常能写。
func TestRequireActiveLetsNormalUserThrough(t *testing.T) {
	code, _, _, reached := serveWithActive(t, fakeBan{})
	if code != http.StatusOK || !reached {
		t.Fatalf("状态码 = %d，handler 是否执行 = %v，期望 200 且执行", code, reached)
	}
}

// TestRequireActiveBlocksBannedUser 验证封禁中的用户被拦在 handler 之前。
//
// 三个断言各有分工：403 是给客户端的语义，banned 是给前端分支的机读码
// （它要据此把发布按钮置灰，而不是弹一句笼统的「没有权限」），
// 解除时间必须出现在文案里——用户最想知道的就是什么时候能再发。
func TestRequireActiveBlocksBannedUser(t *testing.T) {
	until := time.Now().Add(48 * time.Hour)
	code, errorCode, message, reached := serveWithActive(t, fakeBan{until: until})

	if code != http.StatusForbidden {
		t.Fatalf("状态码 = %d，期望 403", code)
	}
	if reached {
		t.Fatal("被封禁的用户不该进入 handler")
	}
	if errorCode != "banned" {
		t.Errorf("error_code = %q，期望 banned", errorCode)
	}
	if !strings.Contains(message, until.Local().Format("2006-01-02 15:04")) {
		t.Errorf("文案 %q 里没有解除时间", message)
	}
}

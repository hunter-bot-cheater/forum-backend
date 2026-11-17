package article

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/httputil"
	"forum-backend/internal/textutil"
	"forum-backend/pkg/response"
)

// LikedChecker 回答「这个用户点过这篇文章吗」。
//
// 用接口而不是直接 import like 包：like 已经依赖 article（点赞后要调 InvalidateDetailCache），
// article 反过来再依赖 like 就成了循环依赖。实现由 router 在装配期注入 like.LikeService。
type LikedChecker interface {
	HasLiked(ctx context.Context, articleID, userID uint) (bool, error)
}

// ArticleHandler 是文章模块的 HTTP 入口，只依赖同包的 service 与一个点赞查询接口，
// 不认识 GORM，也不认识 like 包的类型。
// 每个方法都走同一个套路：解析参数 → 调 service → 成功交 DTO / 失败交 MapServiceError。
type ArticleHandler struct {
	svc *ArticleService
	// liked 只服务详情页的「我点过没」。游客访问时用不到。
	liked LikedChecker
}

// NewArticleHandler 注入文章 service 与点赞查询，由 router 在装配期调用。
func NewArticleHandler(svc *ArticleService, liked LikedChecker) *ArticleHandler {
	return &ArticleHandler{svc: svc, liked: liked}
}

// List godoc
// @Summary      文章分页列表
// @Description  可按作者、关键词或标签筛选；关键词同时匹配标题与正文，不区分大小写
// @Tags         articles
// @Produce      json
// @Param        page       query  int     false  "页码，默认 1"
// @Param        page_size  query  int     false  "每页条数，默认 10，最大 50"
// @Param        author_id  query  int     false  "只看某个作者的文章，不传为全站"
// @Param        keyword    query  string  false  "搜索关键词，同时匹配标题与正文，最长 50 字"
// @Param        tag        query  string  false  "只看带这个标签的文章，按名字精确匹配"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=articleListResp}
// @Failure      400  {string}  string    "分页参数越界、author_id 非法、关键词或标签过长"
// @Router       /articles [get]
// List 处理 GET /api/articles，返回当前页数据与总数。
func (h *ArticleHandler) List(c *gin.Context) {
	// 分页参数统一由 httputil.ParsePage 解析：缺省值、上下界、溢出检查都在那一处，
	// 各接口不重复写，口径才不会漂。出错时交给 MapServiceError 统一映射。
	page, pageSize, err := httputil.ParsePage(c)
	if err != nil {
		httputil.MapServiceError(c, "list articles", err)
		return
	}

	// author_id 可选，只在用户主页传。
	// 非法值直接 400 而不是当成 0：静默降级会让前端拼错参数时拿到全站列表，
	// 看起来像「这个人的主页里混进了别人的文章」。
	var authorID uint
	if raw := c.Query("author_id"); raw != "" {
		parsed, parseErr := strconv.ParseUint(raw, 10, 64)
		if parseErr != nil {
			response.Error(c, http.StatusBadRequest, "author_id 格式不正确")
			return
		}
		authorID = uint(parsed)
	}

	// keyword 可选，trim 后为空当作「没搜」：只敲几个空格不该算出一次搜索。
	// 超长在这里判 400 而不是截断：截断会返回与用户输入不符的结果，
	// 静默忽略又会让人以为搜索功能没生效。
	keyword := strings.TrimSpace(c.Query("keyword"))
	if textutil.TooLongRunes(keyword, maxKeywordRunes) {
		response.Error(c, http.StatusBadRequest, "关键词最长 50 个字")
		return
	}

	// tag 可选，按名字精确匹配。与关键词一样 trim 后判长度：
	// 标签本来就短，超过上限只可能是拼错或恶意构造，给 400 比返回空列表更好排查。
	tagName := strings.TrimSpace(c.Query("tag"))
	if textutil.TooLongRunes(tagName, maxTagRunes) {
		response.Error(c, http.StatusBadRequest, "标签最长 32 个字")
		return
	}

	articles, total, err := h.svc.List(c.Request.Context(), authorID, keyword, tagName, page, pageSize)
	if err != nil {
		httputil.MapServiceError(c, "list articles", err)
		return
	}

	// 这里必须从「空切片」开始，而不是声明 var list []articleListItem：
	// nil 切片会被序列化成 null，前端拿到的 list 就不是数组了。
	// 容量先按 len(articles) 申请，避免 append 过程中反复扩容。
	// 列表项不含正文，只带服务端截好的摘要，响应体不会随正文长度膨胀。
	list := make([]articleListItem, 0, len(articles))
	for _, a := range articles {
		list = append(list, toArticleListItem(a))
	}

	resp := articleListResp{
		List:     list,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
	response.OK(c, resp)
}

// ListTags godoc
// @Summary      热门标签
// @Description  按文章数倒序返回前 20 个标签，供前端做标签云与筛选入口
// @Tags         articles
// @Produce      json
// @Success      200  {object}  httputil.SwaggerEnvelope{data=tagListResp}
// @Router       /tags [get]
// ListTags 处理 GET /api/tags。
//
// 这个接口不需要登录：标签云是列表页的公开入口，游客也应该能看到有哪些标签。
func (h *ArticleHandler) ListTags(c *gin.Context) {
	tags, err := h.svc.TopTags(c.Request.Context(), topTagsLimit)
	if err != nil {
		httputil.MapServiceError(c, "list tags", err)
		return
	}
	// TopTags 内部用 Scan 装结果，空表时是空切片而不是 nil，可以直接下发。
	response.OK(c, tagListResp{List: tags})
}

// ListTagsForAdmin godoc
// @Summary      标签管理列表
// @Description  管理员用：分页列出全部标签（含没人使用的），带 id 与文章数，可按名字模糊搜索
// @Tags         admin
// @Produce      json
// @Param        keyword    query  string  false  "按名字模糊匹配"
// @Param        page       query  int     false  "页码，从 1 开始"
// @Param        page_size  query  int     false  "每页条数，1~50"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=tagAdminListResp}
// @Failure      400  {string}  string  "分页参数不合法"
// @Failure      403  {string}  string  "不是管理员"
// @Router       /admin/tags [get]
// ListTagsForAdmin 处理 GET /api/admin/tags。
//
// 与公开的 /tags 分开：那个只回前 20 个热门标签、不带 id，
// 而管理端要能翻到冷门标签、也要拿到 id 才能发起改名。
func (h *ArticleHandler) ListTagsForAdmin(c *gin.Context) {
	page, pageSize, err := httputil.ParsePage(c)
	if err != nil {
		// 分页参数错误也走 MapServiceError：它认识 ErrInvalidInput，
		// 和 service 里被 paging.New 挡下的错误是同一个映射路径。
		httputil.MapServiceError(c, "list tags for admin", err)
		return
	}

	items, total, err := h.svc.ListTagsForAdmin(c.Request.Context(), c.Query("keyword"), page, pageSize)
	if err != nil {
		httputil.MapServiceError(c, "list tags for admin", err)
		return
	}
	response.OK(c, tagAdminListResp{
		List:     items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	})
}

// RenameTag godoc
// @Summary      标签改名或合并
// @Description  管理员用：把标签改成新名字；若新名字已被占用，则把两个标签合并
// @Tags         admin
// @Accept       json
// @Produce      json
// @Param        id    path  int           true  "标签 ID"
// @Param        body  body  renameTagReq  true  "新名字"
// @Success      200   {object}  httputil.SwaggerEnvelope{data=TagRenameResult}
// @Failure      400   {string}  string  "名字为空、超长，或 id 格式不正确"
// @Failure      403   {string}  string  "不是管理员"
// @Failure      404   {string}  string  "标签不存在"
// @Router       /admin/tags/{id} [put]
// RenameTag 处理 PUT /api/admin/tags/:id。
//
// 返回里的 merged 说明这次是改名还是合并，moved_articles 是受影响的文章数。
// 两个数字都给管理员看：合并一个用了两年的标签会一次性改掉几千篇文章的分类，
// 这不该是一个看不出后果的静默操作。
func (h *ArticleHandler) RenameTag(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}

	var req renameTagReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "请求体格式不正确")
		return
	}

	result, err := h.svc.RenameTag(c.Request.Context(), uint(id), req.Name)
	if err != nil {
		httputil.MapServiceError(c, "rename tag", err)
		return
	}
	response.OK(c, result)
}

// Detail godoc
// @Summary      文章详情
// @Tags         articles
// @Produce      json
// @Param        id   path  int  true  "文章 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=articleDetailResp}
// @Failure      400  {string}  string    "id 格式不正确"
// @Failure      404  {string}  string    "文章不存在"
// @Router       /articles/{id} [get]
// Detail 处理 GET /api/articles/:id，缓存命中时不会碰数据库。
func (h *ArticleHandler) Detail(c *gin.Context) {
	// 路径参数是字符串，先转成数字再交给 service。
	// ParseUint 的第二个参数 10 表示十进制，第三个 64 表示位宽；
	// 转换失败（传了字母、负数、超大数）直接在边界挡掉，不会把脏值带进 SQL 条件。
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}

	a, err := h.svc.Detail(c.Request.Context(), uint(id))
	if err != nil {
		// service 已经把「查不到」统一成 domain.ErrNotFound，这里不需要认识 GORM 的错误。
		httputil.MapServiceError(c, "article detail", err)
		return
	}
	// 详情比列表多一个 like_count、view_count 与 liked，所以用另一个 DTO 包一层。
	resp := toArticleDetailResp(a)

	// 同一个身份后面还要用来查「我点过没」，先取一次，别在两处各取一遍。
	userID, loggedIn := httputil.CurrentUserID(c)

	// 浏览数在这里计，而不是在 service 的 Detail 里：只有「详情接口被请求」
	// 才算一次浏览，内部调用（编辑后回读、测试）不该涨这个数字。
	// 登录用户按 id 去重，游客按 IP：两个前缀不能共用，否则 4 号用户
	// 和「IP 末尾是 4」的游客会互相把对方的标记占掉。
	//
	// 放在 Detail 成功之后：文章不存在时不该往 Redis 写去重标记，
	// 否则刷随机 id 就能批量制造要 30 分钟才过期的垃圾 key。
	viewer := "ip:" + c.ClientIP()
	if loggedIn {
		viewer = "u:" + strconv.FormatUint(uint64(userID), 10)
	}
	if err := h.svc.RecordView(c.Request.Context(), uint(id), viewer); err != nil {
		// 浏览数记不上不该让整篇文章打不开：正文、作者、点赞都已经拿到了。
		slog.WarnContext(c.Request.Context(), "record article view",
			"article_id", id, "viewer", viewer, "error", err)
	}

	// 用库里的最新值覆盖缓存里的浏览数。缓存值是回填那一刻的快照，
	// 直接展示会让同一读者刷新两次看到数字变小（第一次回源读到旧值 + 本次计数的补偿，
	// 第二次命中缓存，快照还是更早的）。查询失败就沿用缓存值，不因此让详情报错。
	if n, err := h.svc.ViewCount(c.Request.Context(), uint(id)); err != nil {
		slog.WarnContext(c.Request.Context(), "read article view count",
			"article_id", id, "error", err)
	} else {
		resp.ViewCount = n
	}

	// liked 不能跟着文章进缓存：缓存里存的是文章本身（含 like_count、view_count），
	// 「我点过没」每个用户都不同，只能按当前访问者实时查。
	// 路由挂了 OptionalJWTAuth，没登录或 token 过期时 CurrentUserID 返回 false，
	// 此时保持 false——游客没有可判断的身份。
	if loggedIn && h.liked != nil {
		liked, err := h.liked.HasLiked(c.Request.Context(), uint(id), userID)
		if err != nil {
			// 点赞状态查不出来不该让整篇文章打不开：正文已经拿到了。
			// 这里降级成「未点赞」，用户真点了会走 Like 接口，那边返回的才是真实状态。
			slog.WarnContext(c.Request.Context(), "check liked for article detail",
				"article_id", id, "user_id", userID, "error", err)
		} else {
			resp.Liked = liked
		}
	}

	response.OK(c, resp)
}

// SuggestTag godoc
// @Summary      写作页标签建议
// @Description  用户在写作页边写边调，提前拿到一个建议标签；只返回结果，不落库
// @Tags         articles
// @Accept       json
// @Produce      json
// @Param        body body tagSuggestReq true "标题与正文，至少有一项"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=tagSuggestResp}
// @Failure      429  {string}  string    "本小时的建议次数已用完"
// @Router       /articles/tag-suggest [post]
// SuggestTag 处理 POST /api/articles/tag-suggest。
//
// 放在发布之前调用是为了把「模型冷启动」挪出发布链路：
// 用户在写的时候先问一次，点发布时要么已经采用建议（同步带上），
// 要么由后台 worker 补，两条路都不让作者等模型。
func (h *ArticleHandler) SuggestTag(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		// 理论上挂了这个路由就一定有登录态；写出来是为了不出现 0 号用户去刷配额。
		response.ErrorWithCode(c, http.StatusUnauthorized, response.ErrCodeUnauthorized, "未登录")
		return
	}

	var req tagSuggestReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeBadRequest, "请求体格式不正确")
		return
	}

	// 标题在这里兜一下长度：正文由 service 截断，而标题会直接进 prompt，
	// 放任几十 KB 的标题等于让客户端决定我们要发多大的请求给模型。
	if textutil.TooLongRunes(req.Title, maxKeywordRunes) {
		response.Error(c, http.StatusBadRequest, "标题过长")
		return
	}

	tag, err := h.svc.SuggestTagForDraft(c.Request.Context(), userID, req.Title, req.Content)
	if err != nil {
		// 只有配额用完与 Redis 故障会走到这里，映射成 429 / 500。
		httputil.MapServiceError(c, "suggest tag", err)
		return
	}
	response.OK(c, tagSuggestResp{Tag: tag})
}

// Create godoc
// @Summary      发布文章
// @Description  tags 留空时，若服务端开启了 llm.enabled，会自动生成一个标签；生成失败不影响发布
// @Tags         articles
// @Accept       json
// @Produce      json
// @Param        body body createArticleReq true "标题、正文与标签（标签可为空数组）"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=articleResp}
// @Failure      400  {string}  string    "请求体格式不正确"
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /articles [post]
// Create 处理 POST /api/articles，作者取自 token，不接受客户端指定。
func (h *ArticleHandler) Create(c *gin.Context) {
	// 从 Gin context 取身份。GetUint 在类型不对或没值时返回 0，
	// httputil.CurrentUserID 把 0 判成失败，避免把 0 号用户写进数据库。
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	// 只往两个字段的结构体里绑。请求体里额外的 user_id 字段
	// 会被 encoding/json 直接忽略，伪造作者身份这条路走不通。
	var req createArticleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		// JSON 语法错、字段类型错、body 超过 limitBody 的 1 MiB，都落在这里。
		response.Error(c, http.StatusBadRequest, "请求体格式不正确")
		return
	}

	// 三个入参里，只有后三个来自客户端；userID 来自中间件校验过的 token。
	a, err := h.svc.Create(c.Request.Context(), userID, req.Title, req.Content, req.Tags)
	if err != nil {
		// 标题正文为空、超长、标签过多或过长都会走到这里，由 MapServiceError 映射成 400。
		httputil.MapServiceError(c, "create article", err)
		return
	}

	// 只把 DTO 交给响应包，数据库模型不会直接出现在响应里。
	response.OK(c, toArticleResp(a))
}

// Update godoc
// @Summary      编辑文章
// @Description  只允许作者本人；标题与正文都必填，规则与发布完全一致。
// @Tags         articles
// @Accept       json
// @Produce      json
// @Param        id   path  int  true  "文章 ID"
// @Param        body body createArticleReq true "标题与正文"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=articleDetailResp}
// @Failure      400  {string}  string    "id 或请求体格式不正确、标题正文不合法"
// @Failure      401  {string}  string    "未登录"
// @Failure      404  {string}  string    "文章不存在或不属于当前用户"
// @Security     Bearer
// @Router       /articles/{id} [put]
// Update 处理 PUT /api/articles/:id，返回编辑后的详情（含点赞数）。
func (h *ArticleHandler) Update(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}

	// 与发布共用同一个请求体：能改的字段完全一样（标题、正文），
	// 各写一个结构体只会让两边的绑定与校验慢慢分叉。
	var req createArticleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "请求体格式不正确")
		return
	}

	a, err := h.svc.Update(c.Request.Context(), uint(id), userID, req.Title, req.Content, req.Tags)
	if err != nil {
		// 不存在、不是作者都映射成 404；标题正文或标签不合法映射成 400。
		httputil.MapServiceError(c, "update article", err)
		return
	}

	// 返回详情形状：前端保存后可以整体替换本地状态，不用再补发一次 GET。
	response.OK(c, toArticleDetailResp(a))
}

// Delete 处理 DELETE /api/articles/:id，仅作者本人可删。
func (h *ArticleHandler) Delete(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	// 路径参数先转成数字。非法 id 在 handler 边界挡掉，
	// 不把原始字符串拼进 SQL 条件。
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}

	// 「是不是作者」的判断放在 service 的 WHERE 里由数据库完成，handler 不查库，
	// 避免多一次往返，也避免查完再删之间的竞态。
	if err := h.svc.Delete(c.Request.Context(), uint(id), userID); err != nil {
		httputil.MapServiceError(c, "delete article", err)
		return
	}

	// 当前 response 包只有 OK(200)，所以删除成功返回 200 且 data 为 null。
	// 00 章契约里的 204 属于目标状态，要等响应包加 NoContent 之后一起改。
	response.OK(c, nil)
}

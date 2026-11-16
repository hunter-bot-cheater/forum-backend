// Package message 一对一的私信：发消息、会话列表、消息分页、已读标记、未读总数。
//
// 设计上只有一个「会话」概念，没有群聊：两个人之间最多一条 conversations 记录，
// 由归一化的 pair_key 唯一索引保证。发消息时自动建会话，前端不需要先「申请会话」。
//
// 未读数是会话行上的两个计数列，不在读的时候 count messages：
// 会话列表一页 20 条，实时 count 就是 20 次扫描，而计数的更新点和消息写入
// 在同一事务里，不额外引入不一致。
package message

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"forum-backend/internal/domain"
	"forum-backend/internal/event"
	"forum-backend/internal/model"
	"forum-backend/internal/user"
)

// maxContentRunes 是一条私信的字数上限，和 model.Message 的 size:1000 对齐。
//
// 用字符数而不是字节数：MySQL 的 varchar(1000) 按字符算，
// 中文一条占一个字符但三个字节，按字节判会把 334 个汉字误判成超长。
const maxContentRunes = 1000

// maxClientMsgIDLen 是幂等键的长度上限，和 model.Message 的 size:64 对齐。
// 客户端一般传 UUID（36 字符），留一倍余量。
const maxClientMsgIDLen = 64

// maxMessageLimit 是消息列表一次最多取多少条。
// 和 httputil.ParsePage 的上限一致，避免出现「翻历史一页拉一千条」的请求。
const maxMessageLimit = 50

// Service 是私信模块的业务逻辑。
// 展示信息统一从 user.Briefs 取，和关注、通知共用同一份口径。
type Service struct {
	db    *gorm.DB
	users *user.UserService
}

// NewService 由 router 在装配期调用一次。
func NewService(db *gorm.DB, users *user.UserService) *Service {
	return &Service{db: db, users: users}
}

// ConversationItem 是会话列表里的一行。
// LastMessage 为 nil 表示会话存在但拿不到最后一条消息（理论上不会发生，
// 会话只在发第一条消息时创建），前端渲染时判空即可。
type ConversationItem struct {
	Peer        user.Brief
	LastMessage *model.Message
	Unread      int
	UpdatedAt   time.Time
}

// MessageItem 是消息列表里的一行。
// Mine 由服务端算好，前端不用拿自己的 id 和 SenderID 比。
type MessageItem struct {
	Message model.Message
	Mine    bool
}

// Send 发送一条私信，会话不存在时自动创建。
//
// clientMsgID 是客户端为「这一次发送」生成的幂等键，传空字符串表示不做幂等。
// 为什么需要它：用户点了发送、请求其实已经写入成功，但响应在路上丢了，
// 客户端重试就会多出一条一模一样的消息；聊天记录是长期留存的，
// 重复消息没法像列表那样「刷新一下就没了」，只能靠服务端去重。
//
// 返回创建后的消息，handler 直接转成 DTO 回给前端，
// 前端不用再拉一次列表。重复提交时返回的是第一次那条，id 相同。
func (s *Service) Send(ctx context.Context, senderID, receiverID uint, content, clientMsgID string) (*model.Message, error) {
	// 自己和自己的会话会把一对一的语义搅乱（会话里两个人是同一个），
	// 而且未读数会加到给自己发消息的那个方向，永远读不掉。
	if senderID == receiverID {
		return nil, fmt.Errorf("%w: send message", domain.ErrSelfAction)
	}

	// 收件人必须存在。表上没有外键，这一句是唯一的来源。
	// 不查发件人：发件人来自 token，一定存在。
	if _, err := s.users.GetByID(ctx, receiverID); err != nil {
		return nil, err
	}

	content = normalizeContent(content)
	if content == "" {
		return nil, fmt.Errorf("%w: content is required", domain.ErrInvalidInput)
	}
	if utf8.RuneCountInString(content) > maxContentRunes {
		return nil, fmt.Errorf("%w: content exceeds %d characters", domain.ErrInvalidInput, maxContentRunes)
	}

	// 幂等键按「没传」和「传了」两种情况走两条路，数据库里必须存 NULL 而不是 ''，
	// 否则空串会撞上唯一索引（原因见 model.Message.ClientMsgID 的注释）。
	clientMsgID = strings.TrimSpace(clientMsgID)
	if utf8.RuneCountInString(clientMsgID) > maxClientMsgIDLen {
		return nil, fmt.Errorf("%w: client_msg_id exceeds %d characters", domain.ErrInvalidInput, maxClientMsgIDLen)
	}
	var key *string
	if clientMsgID != "" {
		key = &clientMsgID
	}

	small, large := orderPair(senderID, receiverID)
	now := time.Now()

	var msg model.Message
	// 三条写入必须同生共死：消息本身、会话的最后一条指针、收件人的未读数。
	// 少提交一条的表现分别是「消息丢了」「列表里看不到最新消息」「有红点但没消息」，
	// 每一个都不会报错，只会让用户觉得系统坏了。
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		conv, err := ensureConversation(tx, small, large, now)
		if err != nil {
			return err
		}

		// 常规重试路径：上一次已经提交成功，这里一查就命中，直接把原消息还回去。
		// 命中时直接 return，不走下面的写入，也不碰未读数——
		// 重试不是一条新消息，给它 +1 会让红点数字越点越大。
		if key != nil {
			old, err := findMessageByClientID(tx, conv.ID, *key, false)
			if err != nil {
				return err
			}
			if old != nil {
				msg = *old
				return nil
			}
		}

		msg = model.Message{
			ConversationID: conv.ID,
			SenderID:       senderID,
			ReceiverID:     receiverID,
			ClientMsgID:    key,
			Content:        content,
			CreatedAt:      now,
		}
		if err := tx.Create(&msg).Error; err != nil {
			// 两个相同的请求同时进来：都过了上面的查询、都去插入，
			// 由 uk_msg_client 挡住后到的那个。这里把它翻译成「读回已存在的那条」，
			// 语义仍是幂等成功，不该给用户回 500。
			// 这个分支只在钥匙不为空时可能发生，没有幂等键的请求本来就不去重。
			if key != nil && errors.Is(err, gorm.ErrDuplicatedKey) {
				old, reloadErr := findMessageByClientID(tx, conv.ID, *key, true)
				if reloadErr != nil {
					return reloadErr
				}
				if old != nil {
					msg = *old
					return nil
				}
			}
			return fmt.Errorf("create message: %w", err)
		}

		// 未读数只能给收件人那一列加。用 Expr 做数据库侧自增而不是先读再写：
		// 两个人同时给对方发消息时，读到旧值再写会丢掉其中一次递增。
		updates := map[string]any{
			"last_message_id":              msg.ID,
			"last_message_at":              now,
			unreadColumn(conv, receiverID): gorm.Expr(unreadColumn(conv, receiverID) + " + 1"),
		}
		if err := tx.Model(&model.Conversation{}).Where("id = ?", conv.ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("bump conversation: %w", err)
		}

		// 事件和消息同事务提交：收件人能不能立刻看到这条消息取决于它，
		// 但它读的是 MySQL 里的历史，所以推送失败不影响消息本身。
		// 走 outbox 而不是在 handler 里直接 hub.Push：进程重启、多实例部署时，
		// 直推只能覆盖「和发送者连在同一台机器上」的收件人。
		if err := event.EmitCreated(ctx, tx, event.TypeMessageCreated, senderID,
			event.Target{Type: event.TargetUser, ID: receiverID},
			&event.Target{Type: event.TargetMessage, ID: msg.ID},
		); err != nil {
			return fmt.Errorf("emit message event: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

// findMessageByClientID 按幂等键找一条已存在的消息，没有返回 (nil, nil)。
//
// currentRead 为 true 时用 FOR UPDATE 走当前读。为什么「撞了唯一索引之后补查」
// 必须走当前读：MySQL 默认 RR，事务里第一次普通 SELECT 就定下了快照，
// 后来别的事务提交的行在快照里看不见，补查会得到「明明撞键了，却查不到那一行」。
// 当前读直接看最新已提交版本，和 ensureConversation 里补查会话是同一个道理。
func findMessageByClientID(tx *gorm.DB, convID uint, clientMsgID string, currentRead bool) (*model.Message, error) {
	q := tx.Where("conversation_id = ? AND client_msg_id = ?", convID, clientMsgID)
	if currentRead {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}

	var m model.Message
	err := q.First(&m).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find message by client id: %w", err)
	}
	return &m, nil
}

// Conversations 返回我的会话列表，按最后消息时间倒序。
//
// 固定 3 次查询：总数、当前页会话、批量取对方信息与最后一条消息，与页大小无关。
func (s *Service) Conversations(ctx context.Context, userID uint, page, pageSize int) ([]ConversationItem, int64, error) {
	if err := validatePaging(page, pageSize); err != nil {
		return nil, 0, err
	}

	// 「我参与的会话」天然是两列之一，用 OR。两个 (user, last_message_at)
	// 复合索引都能覆盖到自己那一半，MySQL 走 index merge 后统一排序。
	mine := func(db *gorm.DB) *gorm.DB {
		return db.Where("user_a_id = ? OR user_b_id = ?", userID, userID)
	}

	var total int64
	if err := mine(s.db.WithContext(ctx).Model(&model.Conversation{})).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count conversations: %w", err)
	}

	// 初始化成空切片：页越界时 Find 不填充，JSON 仍是 [] 而不是 null。
	convs := make([]model.Conversation, 0, pageSize)
	err := mine(s.db.WithContext(ctx)).
		Order("last_message_at DESC, id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Find(&convs).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list conversations: %w", err)
	}
	if len(convs) == 0 {
		return []ConversationItem{}, total, nil
	}

	// 一次收集本页要用的两类 id，之后两次批量查询解决，避免 2N 次往返。
	peerIDs := make([]uint, 0, len(convs))
	lastMsgIDs := make([]uint, 0, len(convs))
	for i := range convs {
		peerIDs = append(peerIDs, peerOf(&convs[i], userID))
		if convs[i].LastMessageID != 0 {
			lastMsgIDs = append(lastMsgIDs, convs[i].LastMessageID)
		}
	}

	briefs, err := s.users.Briefs(ctx, peerIDs)
	if err != nil {
		return nil, 0, err
	}

	lastMsgs := make(map[uint]*model.Message, len(lastMsgIDs))
	if len(lastMsgIDs) > 0 {
		var rows []model.Message
		if err := s.db.WithContext(ctx).Where("id IN ?", lastMsgIDs).Find(&rows).Error; err != nil {
			return nil, 0, fmt.Errorf("load last messages: %w", err)
		}
		for i := range rows {
			lastMsgs[rows[i].ID] = &rows[i]
		}
	}

	// 按会话顺序组装。不能遍历 map：map 的遍历顺序是随机的。
	items := make([]ConversationItem, 0, len(convs))
	for i := range convs {
		conv := &convs[i]
		peerID := peerOf(conv, userID)
		brief, ok := briefs[peerID]
		if !ok {
			// 对方账号已删但会话还在：跳过这一行，不要让整页失败。
			continue
		}
		items = append(items, ConversationItem{
			Peer:        brief,
			LastMessage: lastMsgs[conv.LastMessageID],
			Unread:      unreadOf(conv, userID),
			UpdatedAt:   conv.LastMessageAt,
		})
	}
	return items, total, nil
}

// MessageQuery 是「取一页历史消息」的参数。
//
// BeforeID 为 0 表示从最新一条往回取；否则只取 id 更小（即更早）的消息。
// 用游标而不是页码，是因为聊天记录会一直往后追加：
// 用户正在往上翻历史时对方又发了新消息，OFFSET 会整体往后挪，
// 第二页就会把第一页已经看过的消息再返回一遍（或者跳过几条），
// 而 id 是稳定且唯一的，拿「本页最早一条的 id」当锚点就不会漂。
type MessageQuery struct {
	BeforeID uint
	Limit    int
}

// MessagePage 是一次「进入和某人的聊天页」所需的全部数据。
//
// 把对方资料和消息一起回，是为了让前端进聊天页只发一个请求：
// 分开取的话，首屏要等两个请求都回来才能画（头像、昵称在顶部，消息在下面），
// 慢的一方决定页面多久能看。会话还不存在时 Peer 同样有值，
// 空聊天页的顶栏也能正常显示对方是谁。
//
// HasMore 与 NextBeforeID 一起构成翻页协议：前端拿 NextBeforeID 作为下一次的
// before_id 继续往上翻，HasMore 为 false 就说明到头了，不用再多发一次空请求。
type MessagePage struct {
	Items        []MessageItem
	Peer         user.Brief
	HasMore      bool
	NextBeforeID uint
}

// Messages 返回我与某人的一页消息，页内按时间正序（最早在前）。
//
// 取数方向是倒序（从新到旧）取 Limit+1 条：多取的那一条只用来判断
// 「还有没有更早的」，不返回给前端。这样每翻一页固定一条 SQL，
// 不需要额外的 COUNT，也不会出现「最后一页正好取满、前端以为还有」的误判。
// 组装时再反转成正序，前端可以直接从上到下渲染，不用自己 reverse。
func (s *Service) Messages(ctx context.Context, userID, peerID uint, q MessageQuery) (*MessagePage, error) {
	if err := validateMessageQuery(q); err != nil {
		return nil, err
	}
	// 查对方的存在性和展示信息用同一次查询：GetByID 已经把整行取出来了，
	// 再调一次 Briefs 就是白多一次往返。
	peerUser, err := s.users.GetByID(ctx, peerID)
	if err != nil {
		return nil, err
	}
	page := &MessagePage{Items: []MessageItem{}, Peer: user.NewBrief(peerUser)}

	// 还没聊过时不该回 404：会话不存在等于「没有消息」，
	// 前端拿到空列表就够判断，回 404 会让「点进陌生人的私信页」变成错误页。
	conv, err := s.findConversation(ctx, userID, peerID)
	if err != nil {
		return nil, err
	}
	if conv == nil {
		return page, nil
	}

	dbQuery := s.db.WithContext(ctx).Where("conversation_id = ?", conv.ID)
	if q.BeforeID > 0 {
		dbQuery = dbQuery.Where("id < ?", q.BeforeID)
	}

	// 走 idx_msg_conv：InnoDB 二级索引末尾自带主键，所以 (conversation_id, id DESC)
	// 是一次有序的范围扫描，不需要额外排序，也不需要单独的 id 索引。
	rows := make([]model.Message, 0, q.Limit+1)
	err = dbQuery.Order("id DESC").Limit(q.Limit + 1).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}

	// 多出来的那条只用于判断是否还有更早的消息，处理完就丢掉。
	page.HasMore = len(rows) > q.Limit
	if page.HasMore {
		rows = rows[:q.Limit]
	}

	items := make([]MessageItem, 0, len(rows))
	// 从尾到头遍历，等于把倒序结果翻成正序，省掉一次 reverse。
	for i := len(rows) - 1; i >= 0; i-- {
		items = append(items, MessageItem{
			Message: rows[i],
			Mine:    rows[i].SenderID == userID,
		})
	}
	page.Items = items
	if len(rows) > 0 {
		// rows 此时仍是倒序，最后一个元素就是本页最早的那条。
		page.NextBeforeID = rows[len(rows)-1].ID
	}
	return page, nil
}

// MarkRead 把「我和某人」的会话标记为已读，返回这次清掉了多少条未读。
//
// 幂等：已经全部读过时返回 0，不报错。前端可以在打开聊天窗口时无脑调用。
func (s *Service) MarkRead(ctx context.Context, userID, peerID uint) (int64, error) {
	conv, err := s.findConversation(ctx, userID, peerID)
	if err != nil {
		return 0, err
	}
	if conv == nil {
		return 0, nil
	}

	var cleared int64
	// 两处状态必须同时改：消息行的 read_at 和会话行的未读计数。
	// 只改计数的话，以后按「未读消息」查历史会对不上；
	// 只改 read_at 的话，红点永远消不掉。
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		res := tx.Model(&model.Message{}).
			Where("conversation_id = ? AND receiver_id = ? AND read_at IS NULL", conv.ID, userID).
			Update("read_at", now)
		if res.Error != nil {
			return fmt.Errorf("mark messages read: %w", res.Error)
		}
		cleared = res.RowsAffected

		if err := tx.Model(&model.Conversation{}).Where("id = ?", conv.ID).
			Update(unreadColumn(conv, userID), 0).Error; err != nil {
			return fmt.Errorf("reset unread: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return cleared, nil
}

// UnreadTotal 返回我的总未读私信数，供顶部角标显示。
//
// 一条聚合 SQL 而不是先取会话再逐个加：会话多了之后两次往返会变成几十次。
// inner join 不需要，未读数本来就存在会话行上。
func (s *Service) UnreadTotal(ctx context.Context, userID uint) (int64, error) {
	var total int64
	// IF 而不是 CASE：MySQL 里两者等价，IF 更短；参数全是占位符，不存在拼接。
	err := s.db.WithContext(ctx).Raw(
		`SELECT COALESCE(SUM(IF(user_a_id = ?, unread_a, unread_b)), 0)
		   FROM conversations
		  WHERE user_a_id = ? OR user_b_id = ?`,
		userID, userID, userID,
	).Scan(&total).Error
	if err != nil {
		return 0, fmt.Errorf("sum unread: %w", err)
	}
	return total, nil
}

// findConversation 按 pair_key 找会话，不存在返回 (nil, nil)。
//
// 不返回 ErrNotFound：调用方分三种情况处理（列表给空、已读给 0），
// 统一成一个错误会让每个调用点都要 errors.Is 一次。
func (s *Service) findConversation(ctx context.Context, userID, peerID uint) (*model.Conversation, error) {
	small, large := orderPair(userID, peerID)
	var conv model.Conversation
	err := s.db.WithContext(ctx).Where("pair_key = ?", pairKey(small, large)).First(&conv).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find conversation: %w", err)
	}
	return &conv, nil
}

// ensureConversation 取会话，没有就建。必须在事务里调用。
//
// 并发建会话的竞态交给唯一索引：两个请求同时 First 都查不到、同时 Create，
// 后到的那个被 uk_conv_pair 挡住。用 ON DUPLICATE KEY DO NOTHING 把这次冲突
// 变成静默跳过，再查一次拿到先建的那一行，这样两边都拿到同一个 id，
// 后续写消息不会分裂到两条会话上。
func ensureConversation(tx *gorm.DB, small, large uint, now time.Time) (*model.Conversation, error) {
	key := pairKey(small, large)

	var conv model.Conversation
	err := tx.Where("pair_key = ?", key).First(&conv).Error
	if err == nil {
		return &conv, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("find conversation: %w", err)
	}

	// 首次创建：LastMessageAt 先用当前时间占位，同一事务里会被真实消息时间覆盖。
	// 不设成零值是为了让「刚建的会话」在排序时不会掉到最后。
	conv = model.Conversation{
		PairKey:       key,
		UserAID:       small,
		UserBID:       large,
		LastMessageAt: now,
	}
	res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&conv)
	if res.Error != nil {
		return nil, fmt.Errorf("create conversation: %w", res.Error)
	}

	// 冲突时 GORM 不会回填 id，需要再查一次拿到先建的那一行。
	//
	// 这次必须用当前读（FOR UPDATE）而不是普通 SELECT：
	// MySQL 默认隔离级别是 REPEATABLE READ，普通 SELECT 走的是事务开始时
	// 建立的快照，看不到并发事务刚刚提交的那一行。拿快照读去补查，
	// 会得到「刚被别人插入了，但我这里查不到」的结果，整个事务回滚，
	// 表现就是双方同时发第一条消息时其中一条静默失败（没有报错，消息就是没了）。
	// 当前读绕过快照，直接看已提交的最新版本。
	//
	// 加锁只在「首次创建会话」这个一次性路径上，之后都走前面那次普通查询。
	if conv.ID == 0 {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("pair_key = ?", key).First(&conv).Error
		if err != nil {
			return nil, fmt.Errorf("reload conversation: %w", err)
		}
	}
	return &conv, nil
}

// pairKey 拼出归一化的会话键，形如 "3:7"（小 id 在前）。
//
// 归一化是这套设计的核心：A 给 B 发和 B 给 A 发必须落到同一个 key，
// 否则会话列表里会出现两个条目互相跳，未读数也会分裂成两份。
func pairKey(small, large uint) string {
	return strconv.FormatUint(uint64(small), 10) + ":" + strconv.FormatUint(uint64(large), 10)
}

// orderPair 把两个 id 排成 (小, 大)，与 Conversation.UserAID / UserBID 的约定一致。
func orderPair(a, b uint) (small, large uint) {
	if a > b {
		return b, a
	}
	return a, b
}

// peerOf 取会话里「另一方」的 id。
func peerOf(conv *model.Conversation, userID uint) uint {
	if conv.UserAID == userID {
		return conv.UserBID
	}
	return conv.UserAID
}

// unreadOf 取会话里某人的未读数。
func unreadOf(conv *model.Conversation, userID uint) int {
	if conv.UserAID == userID {
		return conv.UnreadA
	}
	return conv.UnreadB
}

// unreadColumn 返回某人对应的未读列名，供写路径构造 UPDATE 用。
//
// 返回列名而不是值，是因为调用点要做数据库侧自增（unread_b + 1），
// 拼进去的只能是列名。调用方只传 userID，不传外部字符串，不存在注入。
func unreadColumn(conv *model.Conversation, userID uint) string {
	if conv.UserAID == userID {
		return "unread_a"
	}
	return "unread_b"
}

// normalizeContent 去掉首尾空白。全是空格的正文等于空消息，不该发出去。
// 中间的空格与换行保留：用户可能故意用空行分段。
func normalizeContent(s string) string {
	return strings.TrimSpace(s)
}

// validatePaging 校验会话列表的分页参数。
//
// 和 httputil.ParsePage 的上限保持一致，但这里再判一次：
// service 可能被别的入口调用（以后的 WebSocket、后台脚本），
// 不能指望每个调用方都先过一遍 HTTP 层的解析。
func validatePaging(page, pageSize int) error {
	if page < 1 || pageSize < 1 || pageSize > maxMessageLimit {
		return fmt.Errorf("%w: invalid pagination", domain.ErrInvalidInput)
	}
	return nil
}

// validateMessageQuery 校验消息列表的游标参数。
//
// BeforeID 允许为 0（表示最新一页），负数在 handler 就挡掉了，
// 这里仍判一次是因为 service 可能被非 HTTP 入口直接调用。
func validateMessageQuery(q MessageQuery) error {
	if q.Limit < 1 || q.Limit > maxMessageLimit {
		return fmt.Errorf("%w: invalid limit", domain.ErrInvalidInput)
	}
	return nil
}

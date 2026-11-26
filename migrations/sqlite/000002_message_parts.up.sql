-- 消息片段：按发生顺序保存助手在一次 Run 中的文本、思考与工具调用。
-- 工具片段只保存 tool_calls 的标识，工具详情仍以 tool_calls 表为权威来源。
ALTER TABLE messages ADD COLUMN parts TEXT NOT NULL DEFAULT '[]';

-- 用户与系统消息只有一段正文。
UPDATE messages
SET parts = json_array(json_object('type', 'text', 'text', content))
WHERE role IN ('user', 'system') AND content <> '';

-- 助手消息按 run_events 的 seq 重建真实对话顺序。旧运行一次 Run 只有一条
-- 助手消息，因此可以按 run_id 关联；事件缺失时由下一条语句回退到 content。
-- SQLite 不支持 LATERAL，改用相关子查询：子查询按 seq 排序后聚合，
-- json_group_array 会按行读取顺序保留顺序。
UPDATE messages
SET parts = (
    -- json(part) 强制把 json_object 生成的文本识别为 JSON 值，
    -- 否则 json_group_array 会将其当作普通字符串转义。
    SELECT json_group_array(json(part)) FROM (
        SELECT
            CASE
                WHEN e.type = 'message.delta'
                    AND json_extract(e.payload_json, '$.message_id') = CAST(messages.id AS TEXT)
                    THEN json_object('type', 'text', 'text', json_extract(e.payload_json, '$.delta'))
                WHEN e.type = 'message.reasoning'
                    AND json_extract(e.payload_json, '$.message_id') = CAST(messages.id AS TEXT)
                    THEN json_object('type', 'reasoning', 'text', json_extract(e.payload_json, '$.delta'))
                WHEN e.type = 'tool.started'
                    AND json_extract(e.payload_json, '$.tool_call_id') IS NOT NULL
                    THEN json_object('type', 'tool', 'tool_call_id', json_extract(e.payload_json, '$.tool_call_id'))
                ELSE NULL
            END AS part
        FROM run_events AS e
        WHERE e.run_id = messages.run_id
        ORDER BY e.seq
    )
    WHERE part IS NOT NULL
)
WHERE role = 'assistant' AND run_id IS NOT NULL;

-- 没有事件历史的旧助手消息回退为一整段正文。
UPDATE messages
SET parts = json_array(json_object('type', 'text', 'text', content))
WHERE role = 'assistant' AND parts = '[]' AND content <> '';

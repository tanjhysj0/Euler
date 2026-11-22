-- 消息片段：按发生顺序保存助手在一次 Run 中的文本、思考与工具调用。
-- 工具片段只保存 tool_calls 的标识，工具详情仍以 tool_calls 表为权威来源。
ALTER TABLE messages ADD COLUMN parts jsonb NOT NULL DEFAULT '[]'::jsonb;

-- 用户与系统消息只有一段正文。
UPDATE messages
SET parts = jsonb_build_array(jsonb_build_object('type', 'text', 'text', content))
WHERE role IN ('user', 'system') AND content <> '';

-- 助手消息按 run_events 的 seq 重建真实对话顺序。旧运行一次 Run 只有一条
-- 助手消息，因此可以按 run_id 关联；事件缺失时由下一条语句回退到 content。
UPDATE messages AS m
SET parts = rebuilt.parts
FROM (
    SELECT message.id AS message_id, jsonb_agg(ev.part ORDER BY ev.seq) AS parts
    FROM messages AS message
    JOIN LATERAL (
        SELECT e.seq,
               CASE
                   WHEN e.type = 'message.delta'
                       AND e.payload_json ->> 'message_id' = message.id::text
                       THEN jsonb_build_object(
                           'type', 'text', 'text', e.payload_json ->> 'delta')
                   WHEN e.type = 'message.reasoning'
                       AND e.payload_json ->> 'message_id' = message.id::text
                       THEN jsonb_build_object(
                           'type', 'reasoning', 'text', e.payload_json ->> 'delta')
                   WHEN e.type = 'tool.started'
                       AND e.payload_json ->> 'tool_call_id' IS NOT NULL
                       THEN jsonb_build_object(
                           'type', 'tool',
                           'tool_call_id', e.payload_json ->> 'tool_call_id')
                   ELSE NULL
               END AS part
        FROM run_events AS e
        WHERE e.run_id = message.run_id
    ) AS ev ON ev.part IS NOT NULL
    WHERE message.role = 'assistant' AND message.run_id IS NOT NULL
    GROUP BY message.id
) AS rebuilt
WHERE m.id = rebuilt.message_id;

-- 没有事件历史的旧助手消息回退为一整段正文。
UPDATE messages
SET parts = jsonb_build_array(jsonb_build_object('type', 'text', 'text', content))
WHERE role = 'assistant' AND parts = '[]'::jsonb AND content <> '';

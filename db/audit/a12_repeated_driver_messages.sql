-- id: A12
-- title: The chat channel repeats driver messages
-- impact: The same text arrives on two threads (THR001 and THR009). A retried message is recorded once (by message id) and a second report of the same problem on the same day is stored as DUPLICATE, so it is never planned twice.
SELECT m.chat_message_id, m.thread_id, m.message_text, m.is_duplicate, x.exception_id, x.exception_status
FROM chat_messages m
LEFT JOIN driver_exceptions x ON x.thread_id = m.thread_id
WHERE m.sender_type = 'DRIVER'
  AND m.message_text IN (SELECT message_text FROM chat_messages WHERE sender_type = 'DRIVER'
                         GROUP BY message_text HAVING count(*) > 1)
ORDER BY m.message_ts, m.chat_message_id;

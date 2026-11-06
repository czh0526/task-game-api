import { useEffect, useState } from "react";
import type { UserScores } from "taskgame-client";
import { useApp } from "../context";
import { formatError, formatTime, validUserId } from "../format";
import { PRESETS } from "../settings";
import { Button, ErrorNote, Field, IdText, Screen } from "../ui";

export function Scores() {
  const { client, clientError, settings } = useApp();
  const [userId, setUserId] = useState(settings.userId || PRESETS.publisher.userId);
  const [scores, setScores] = useState<UserScores | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  async function lookup(id: string) {
    if (!client) return;
    const trimmed = id.trim();
    if (!validUserId(trimmed)) {
      setError("用户 id 需要匹配 ^[A-Za-z0-9_.:@-]{1,128}$");
      setScores(null);
      return;
    }
    setPending(true);
    setError(null);
    try {
      setScores(await client.getUserScores(trimmed));
    } catch (err) {
      setScores(null);
      setError(formatError(err));
    } finally {
      setPending(false);
    }
  }

  useEffect(() => {
    if (!client) return;
    const initial = settings.userId || PRESETS.publisher.userId;
    setUserId(initial);
    void lookup(initial);
  }, [client, settings.userId]);

  return (
    <Screen title="积分" lede="GET /v1/users/{userId}/scores。字段是 integrity（诚信）和 ability（能力）。未知用户返回 0，不是 404。">
      <ErrorNote message={client ? error : clientError} />
      <form
        className="toolbar"
        onSubmit={(event) => {
          event.preventDefault();
          void lookup(userId);
        }}
      >
        <Field label="user id">
          <input value={userId} spellCheck={false} onChange={(event) => setUserId(event.target.value)} />
        </Field>
        <Button type="submit" kind="primary" pending={pending} disabled={!client}>
          查询
        </Button>
        <Button
          type="button"
          kind="secondary"
          onClick={() => {
            setUserId(PRESETS.publisher.userId);
            void lookup(PRESETS.publisher.userId);
          }}
        >
          usr_publisher
        </Button>
        <Button
          type="button"
          kind="secondary"
          onClick={() => {
            setUserId(PRESETS.executor.userId);
            void lookup(PRESETS.executor.userId);
          }}
        >
          usr_executor
        </Button>
      </form>
      {scores ? (
        <div className="card">
          <p>
            用户 <IdText value={scores.user_id} />
          </p>
          <div className="score-grid">
            <div>
              <span className="fact-label">integrity 诚信</span>
              <strong className="score">{scores.integrity}</strong>
            </div>
            <div>
              <span className="fact-label">ability 能力</span>
              <strong className="score">{scores.ability}</strong>
            </div>
          </div>
          <h2>最近变动</h2>
          {(scores.recent ?? []).length === 0 ? <p className="hint">暂无记录。协作退出也会记一条 0 / 0，原因 collaborative_dropout。</p> : null}
          {(scores.recent ?? []).length > 0 ? (
            <table>
              <thead>
                <tr>
                  <th>时间</th>
                  <th>reason</th>
                  <th>delta_integrity</th>
                  <th>delta_ability</th>
                  <th>task_id</th>
                </tr>
              </thead>
              <tbody>
                {(scores.recent ?? []).map((change, index) => (
                  <tr key={`${change.created_at ?? "t"}-${change.task_id ?? "x"}-${index}`}>
                    <td title={change.created_at}>{formatTime(change.created_at)}</td>
                    <td>
                      <code className="mono">{change.reason ?? "—"}</code>
                    </td>
                    <td>{change.delta_integrity ?? "—"}</td>
                    <td>{change.delta_ability ?? "—"}</td>
                    <td>{change.task_id ? <IdText value={change.task_id} /> : "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : null}
        </div>
      ) : null}
    </Screen>
  );
}

// WP Panel 匿名安装统计 Worker
// POST /api/heartbeat — 面板匿名心跳上报
// GET  /api/stats     — 公开统计（total + active_24h）

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const corsHeaders = {
      'Access-Control-Allow-Origin': '*',
      'Access-Control-Allow-Methods': 'GET, POST, OPTIONS',
      'Access-Control-Allow-Headers': 'Content-Type',
    };

    if (request.method === 'OPTIONS') {
      return new Response(null, { headers: corsHeaders });
    }

    // 公开统计 — 允许网站前端跨域访问
    if (request.method === 'GET' && url.pathname === '/api/stats') {
      const stats = await getStats(env);
      return new Response(JSON.stringify(stats), {
        headers: { ...corsHeaders, 'Content-Type': 'application/json' },
      });
    }

    // 匿名心跳 — 面板定时上报
    if (request.method === 'POST' && url.pathname === '/api/heartbeat') {
      try {
        const body = await request.json();
        const { anonymous_id, version } = body;
        if (!anonymous_id || typeof anonymous_id !== 'string' || anonymous_id.length < 8) {
          return new Response(JSON.stringify({ error: 'invalid anonymous_id' }), {
            status: 400,
            headers: { ...corsHeaders, 'Content-Type': 'application/json' },
          });
        }
        await saveHeartbeat(env, anonymous_id, version || 'unknown');
        return new Response(JSON.stringify({ ok: true }), {
          headers: { ...corsHeaders, 'Content-Type': 'application/json' },
        });
      } catch {
        return new Response(JSON.stringify({ error: 'invalid request' }), {
          status: 400,
          headers: { ...corsHeaders, 'Content-Type': 'application/json' },
        });
      }
    }

    return new Response('Not Found', { status: 404 });
  },
};

async function getStats(env) {
  const today = new Date().toISOString().slice(0, 10);

  // 总数：列出所有 id:* 键
  let total = 0;
  let cursor;
  do {
    const result = await env.STATS_KV.list({ prefix: 'id:', cursor, limit: 1000 });
    total += result.keys.length;
    cursor = result.list_complete ? undefined : result.cursor;
  } while (cursor);

  // 日活：列出今天的心跳键
  let active = 0;
  cursor = undefined;
  do {
    const result = await env.STATS_KV.list({ prefix: `daily:${today}:`, cursor, limit: 1000 });
    active += result.keys.length;
    cursor = result.list_complete ? undefined : result.cursor;
  } while (cursor);

  return { total, active };
}

async function saveHeartbeat(env, anonymousId, version) {
  const now = new Date().toISOString();
  const idKey = `id:${anonymousId}`;

  // 写入/更新单条记录（first 记录首次出现时间）
  const existing = await env.STATS_KV.get(idKey, { type: 'json' });
  await env.STATS_KV.put(idKey, JSON.stringify({
    v: version,
    first: existing?.first || now,
    last: now,
  }));

  // 写入日活标记，48 小时后自动过期
  const today = now.slice(0, 10);
  const dailyKey = `daily:${today}:${anonymousId}`;
  await env.STATS_KV.put(dailyKey, '1', { expirationTtl: 86400 * 2 });
}

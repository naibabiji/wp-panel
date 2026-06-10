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

    // 公开统计 — 直接读计数器，零次 list() 调用
    if (request.method === 'GET' && url.pathname === '/api/stats') {
      const stats = await getStats(env);
      return new Response(JSON.stringify(stats), {
        headers: {
          ...corsHeaders,
          'Content-Type': 'application/json',
          'Cache-Control': 'public, max-age=3600',
        },
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

    // 一次性初始化计数器（迁移后用 curl 调用一次即可删掉这个分支）
    if (request.method === 'POST' && url.pathname === '/api/migrate') {
      await migrateCounters(env);
      return new Response(JSON.stringify({ migrated: true }), {
        headers: { ...corsHeaders, 'Content-Type': 'application/json' },
      });
    }

    return new Response('Not Found', { status: 404 });
  },
};

// 从已有的 id:* / daily:* 键重建 meta:total 和 meta:active 计数器
async function migrateCounters(env) {
  // 重建 total
  let total = 0;
  let cursor;
  do {
    const result = await env.STATS_KV.list({ prefix: 'id:', cursor, limit: 1000 });
    total += result.keys.length;
    cursor = result.list_complete ? undefined : result.cursor;
  } while (cursor);
  await env.STATS_KV.put('meta:total', String(total));

  // 重建日活计数
  const today = new Date().toISOString().slice(0, 10);
  const yesterday = new Date(Date.now() - 86400000).toISOString().slice(0, 10);
  for (const day of [today, yesterday]) {
    let active = 0;
    cursor = undefined;
    do {
      const result = await env.STATS_KV.list({ prefix: `daily:${day}:`, cursor, limit: 1000 });
      active += result.keys.length;
      cursor = result.list_complete ? undefined : result.cursor;
    } while (cursor);
    if (active > 0) {
      await env.STATS_KV.put(`meta:active:${day}`, String(active), { expirationTtl: 129600 });
    }
  }
}

// 仅读取聚合计数器，每次调用 2 次 $get（零次 list）
async function getStats(env) {
  const today = new Date().toISOString().slice(0, 10);

  const [total, activeToday] = await Promise.all([
    env.STATS_KV.get('meta:total'),
    env.STATS_KV.get(`meta:active:${today}`),
  ]);

  return {
    total: parseInt(total) || 0,
    active: parseInt(activeToday) || 0,
  };
}

// 写入心跳时同步更新计数器
async function saveHeartbeat(env, anonymousId, version) {
  const now = new Date().toISOString();
  const today = now.slice(0, 10);
  const idKey = `id:${anonymousId}`;
  const dailyKey = `daily:${today}:${anonymousId}`;

  const [existing, dailyExists] = await Promise.all([
    env.STATS_KV.get(idKey, { type: 'json' }),
    env.STATS_KV.get(dailyKey),
  ]);

  const writes = [];

  writes.push(env.STATS_KV.put(idKey, JSON.stringify({
    v: version,
    first: existing?.first || now,
    last: now,
  })));

  // 新安装 → 总数 +1
  if (!existing) {
    const total = parseInt(await env.STATS_KV.get('meta:total')) || 0;
    writes.push(env.STATS_KV.put('meta:total', String(total + 1)));
  }

  // 今日首次心跳 → 日活 +1
  if (!dailyExists) {
    writes.push(env.STATS_KV.put(dailyKey, '1', { expirationTtl: 129600 }));
    const activeToday = parseInt(await env.STATS_KV.get(`meta:active:${today}`)) || 0;
    writes.push(env.STATS_KV.put(`meta:active:${today}`, String(activeToday + 1), { expirationTtl: 129600 }));
  }

  await Promise.all(writes);
}

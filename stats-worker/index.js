// WP Panel 匿名安装统计 Worker
// POST /api/heartbeat — 面板匿名心跳上报
// GET  /api/stats     — 公开统计（total + 精确 active_24h 滚动窗口）
// GET  /api/ip-ranges/googlebot — Google 官方爬虫 IP 的最后有效缓存

const GOOGLEBOT_UPSTREAM = 'https://developers.google.com/crawling/ipranges/common-crawlers.json';
const GOOGLEBOT_CACHE_KEY = 'ip-ranges:googlebot:v1';

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

    // 公开统计 — 按 id:* 的 last 时间精确计算最近 24 小时活跃数
    if (request.method === 'GET' && url.pathname === '/api/stats') {
      const stats = await getStats(env);
      return new Response(JSON.stringify(stats), {
        headers: {
          ...corsHeaders,
          'Content-Type': 'application/json',
          'Cache-Control': 'public, max-age=300',
        },
      });
    }

    if (request.method === 'GET' && url.pathname === '/api/ip-ranges/googlebot') {
      let snapshot = await env.STATS_KV.get(GOOGLEBOT_CACHE_KEY, { type: 'json' });
      if (!snapshot) {
        try {
          snapshot = await refreshGooglebotRanges(env);
        } catch (error) {
          return jsonResponse({ error: 'googlebot ranges are not cached yet' }, 503, corsHeaders);
        }
      }
      return new Response(JSON.stringify(snapshot.payload), {
        headers: {
          ...corsHeaders,
          'Content-Type': 'application/json',
          'Cache-Control': 'public, max-age=3600, stale-while-revalidate=86400',
          'X-WP-Panel-Fetched-At': snapshot.fetched_at,
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

    // 兼容旧部署：重建 total 快照。/api/stats 不再依赖该计数器。
    if (request.method === 'POST' && url.pathname === '/api/migrate') {
      const stats = await getStats(env);
      await env.STATS_KV.put('meta:total', String(stats.total));
      return new Response(JSON.stringify({ migrated: true }), {
        headers: { ...corsHeaders, 'Content-Type': 'application/json' },
      });
    }

    return new Response('Not Found', { status: 404 });
  },

  async scheduled(_event, env, ctx) {
    ctx.waitUntil(refreshGooglebotRanges(env));
  },
};

function jsonResponse(body, status, headers) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { ...headers, 'Content-Type': 'application/json' },
  });
}

async function refreshGooglebotRanges(env) {
  const response = await fetch(GOOGLEBOT_UPSTREAM, {
    headers: { 'User-Agent': 'WP-Panel-Googlebot-Range-Relay/1.0' },
    cf: { cacheTtl: 3600, cacheEverything: true },
  });
  if (!response.ok) {
    throw new Error(`Google returned HTTP ${response.status}`);
  }
  const payload = await response.json();
  validateGooglebotPayload(payload);
  const snapshot = { fetched_at: new Date().toISOString(), payload };
  await env.STATS_KV.put(GOOGLEBOT_CACHE_KEY, JSON.stringify(snapshot));
  return snapshot;
}

function validateGooglebotPayload(payload) {
  if (!payload || !Array.isArray(payload.prefixes) || payload.prefixes.length === 0 || payload.prefixes.length > 512) {
    throw new Error('invalid Googlebot prefix list');
  }
  for (const prefix of payload.prefixes) {
    const keys = Object.keys(prefix || {});
    if (keys.length !== 1 || !['ipv4Prefix', 'ipv6Prefix'].includes(keys[0])) {
      throw new Error('invalid Googlebot prefix entry');
    }
    const value = prefix[keys[0]];
    if (typeof value !== 'string' || !value.includes('/') || value.length > 80) {
      throw new Error('invalid Googlebot CIDR');
    }
  }
}

async function getStats(env) {
  let total = 0;
  let active = 0;
  const cutoff = Date.now() - 24 * 60 * 60 * 1000;

  let cursor;
  do {
    const result = await env.STATS_KV.list({ prefix: 'id:', cursor, limit: 1000 });
    total += result.keys.length;

    const keysWithoutMetadata = [];
    for (const key of result.keys) {
      const last = key.metadata?.last;
      if (!last) {
        keysWithoutMetadata.push(key);
        continue;
      }
      const lastTime = Date.parse(last);
      if (Number.isFinite(lastTime) && lastTime >= cutoff) {
        active++;
      }
    }

    for (let i = 0; i < keysWithoutMetadata.length; i += 50) {
      const batch = keysWithoutMetadata.slice(i, i + 50);
      const records = await Promise.all(batch.map(async key => {
        try {
          return await env.STATS_KV.get(key.name, { type: 'json' });
        } catch {
          return null;
        }
      }));

      for (const data of records) {
        const lastTime = data?.last ? Date.parse(data.last) : NaN;
        if (Number.isFinite(lastTime) && lastTime >= cutoff) {
          active++;
        }
      }
    }

    cursor = result.list_complete ? undefined : result.cursor;
  } while (cursor);

  return {
    total,
    active_24h: active,
    active: active,
  };
}

// 写入心跳：每个匿名实例保留首次和最近一次上报时间。
async function saveHeartbeat(env, anonymousId, version) {
  const now = new Date().toISOString();
  const idKey = `id:${anonymousId}`;

  const existing = await env.STATS_KV.get(idKey, { type: 'json' });

  const writes = [];

  writes.push(env.STATS_KV.put(
    idKey,
    JSON.stringify({
      v: version,
      first: existing?.first || now,
      last: now,
    }),
    { metadata: { last: now } }
  ));

  // 新安装 → 总数 +1
  if (!existing) {
    const total = parseInt(await env.STATS_KV.get('meta:total')) || 0;
    writes.push(env.STATS_KV.put('meta:total', String(total + 1)));
  }

  await Promise.all(writes);
}

// Cloudflare Worker that receives anonymous telemetry from i2p-vanitygen
// and stores it in a D1 database.
//
// Environment bindings required:
//   DB - D1 database binding

export default {
  async fetch(request, env) {
    // CORS preflight
    if (request.method === "OPTIONS") {
      return new Response(null, {
        status: 204,
        headers: {
          "Access-Control-Allow-Origin": "*",
          "Access-Control-Allow-Methods": "POST, OPTIONS",
          "Access-Control-Allow-Headers": "Content-Type",
          "Access-Control-Max-Age": "86400",
        },
      });
    }

    if (request.method !== "POST") {
      return new Response("Method not allowed", { status: 405 });
    }

    const url = new URL(request.url);
    if (url.pathname !== "/submit") {
      return new Response("Not found", { status: 404 });
    }

    let body;
    try {
      body = await request.json();
    } catch {
      return new Response("Invalid JSON", { status: 400 });
    }

    // Validate schema
    const { prefix_length, duration_seconds, cores_used, attempts } = body;

    if (
      typeof prefix_length !== "number" ||
      !Number.isInteger(prefix_length) ||
      prefix_length < 1 ||
      prefix_length > 52
    ) {
      return new Response("Invalid prefix_length", { status: 400 });
    }
    if (
      typeof duration_seconds !== "number" ||
      duration_seconds < 0 ||
      duration_seconds > 31536000
    ) {
      return new Response("Invalid duration_seconds", { status: 400 });
    }
    if (
      typeof cores_used !== "number" ||
      !Number.isInteger(cores_used) ||
      cores_used < 1 ||
      cores_used > 1024
    ) {
      return new Response("Invalid cores_used", { status: 400 });
    }
    if (
      typeof attempts !== "number" ||
      !Number.isInteger(attempts) ||
      attempts < 1
    ) {
      return new Response("Invalid attempts", { status: 400 });
    }

    // Insert into D1
    await env.DB.prepare(
      "INSERT INTO telemetry (prefix_length, duration_seconds, cores_used, attempts) VALUES (?, ?, ?, ?)"
    )
      .bind(prefix_length, duration_seconds, cores_used, attempts)
      .run();

    return new Response(JSON.stringify({ ok: true }), {
      status: 200,
      headers: {
        "Content-Type": "application/json",
        "Access-Control-Allow-Origin": "*",
      },
    });
  },
};

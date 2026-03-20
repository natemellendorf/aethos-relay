export default {
  async fetch(request) {
    return new Response(
      JSON.stringify({
        service: "aethos-relay",
        status: "ok",
        method: request.method,
      }),
      {
        headers: {
          "content-type": "application/json; charset=utf-8",
        },
      },
    );
  },
};

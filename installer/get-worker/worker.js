// get.slipstreampanel.com — the install script's vanity URL.
//
//   curl -fsSL https://get.slipstreampanel.com | sudo bash
//
// A 302 rather than a 301: the target moves with every release, and a
// permanently-cached redirect in someone's client would pin them to whichever
// release happened to be current the first time they ran it.
const INSTALLER =
  "https://github.com/Sulaiman-Dauda/slipstream/releases/latest/download/install.sh";

export default {
  async fetch(request) {
    const url = new URL(request.url);

    // Allow pinning a version: get.slipstreampanel.com/v0.1.0
    const tag = url.pathname.replace(/^\/+|\/+$/g, "");
    const target = /^v\d+\.\d+\.\d+$/.test(tag)
      ? `https://github.com/Sulaiman-Dauda/slipstream/releases/download/${tag}/install.sh`
      : INSTALLER;

    return new Response(null, {
      status: 302,
      headers: {
        Location: target,
        "Cache-Control": "no-store",
        "Referrer-Policy": "strict-origin-when-cross-origin",
        "X-Content-Type-Options": "nosniff",
      },
    });
  },
};

// The page talks to the JSON API in internal/server/http.go. /api/videos
// marshals store.Video directly, so the JSON keys are the Go field names
// (Title, Author, Pubdate, State, Lang, IsCharging, ...).

// api performs a fetch and returns the parsed JSON body (or null for an empty
// body). A non-2xx response becomes an Error carrying the server's
// {"error": "..."} message.
export async function api(path, options) {
  const res = await fetch(path, options);
  const text = await res.text();
  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = null;
    }
  }
  if (!res.ok) {
    throw new Error(data && data.error ? data.error : `HTTP ${res.status}`);
  }
  return data;
}

export function post(path, body) {
  return api(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

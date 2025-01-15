const eventSource = new EventSource('/events');
const sections = document.querySelectorAll("section");

eventSource.onmessage = function(event) {
    let data;
    let level, time, msg;

    try {
        data = JSON.parse(event.data);
        level, time, msg = JSON.parse(data.Body);
    } catch (e) {
        console.error("Parse:", e);
        console.debug("event.data:", event.data)
        console.debug("data:", data)
        return
    }

    const e = Array.from(sections).filter(s => s.querySelector("h2").textContent == data.Torrent)[0].querySelector("textarea");
    e.value += level + time + msg + "\n";
    e.scrollTop = e.scrollHeight;
};

eventSource.onerror = function() {
    console.error(`Error occurred while receiving SSE`);
    if (eventSource.readyState === EventSource.CLOSED) {
        console.log("Connection closed by server.");
    } else if (eventSource.readyState === EventSource.CONNECTING) {
        console.log("Attempting to reconnect...");
    }
};

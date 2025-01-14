const eventSource = new EventSource('/events');
const sections = document.querySelectorAll("section");

eventSource.onmessage = function(event) {
    const data = JSON.parse(event.data);
    const { level, time, msg } = JSON.parse(data.Body);

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

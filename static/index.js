const eventSource = new EventSource('/events');

eventSource.onmessage = function(event) {
    if (event.data.trim() == "Connected to SSE") {
        console.log("Connected to server")
        return
    }

    try {
        const { Name, Body } = JSON.parse(event.data);
        const { time, level, msg } = JSON.parse(Body);

        const section = document.querySelector(`[data-name="${Name}"]`);
        const grid = section.querySelector(".grid-container");
        const logElem = document.createElement("div")
        logElem.className = "log"
        grid.appendChild(logElem)

        const timeElem = document.createElement("div")
        timeElem.textContent = time
        logElem.appendChild(timeElem)

        const levelElem = document.createElement("div")
        levelElem.textContent = level
        switch (level) {
            case "ERROR":
                levelElem.className += "error";
                break;
            case "WARN":
                levelElem.className += "warn";
                break;
            case "INFO":
                levelElem.className += "info";
                break;
            case "DEBUG":
                levelElem.className += "debug";
                break;
        }
        logElem.appendChild(levelElem)

        const msgElem = document.createElement("div")
        msgElem.textContent = msg
        logElem.appendChild(msgElem)

    } catch (e) {
        console.error(e);
        console.debug("event.data:", event.data)
        return
    }

};

eventSource.onerror = function() {
    console.error(`Error occurred while receiving SSE`);
    if (eventSource.readyState === EventSource.CLOSED) {
        console.log("Connection closed by server.");
    } else if (eventSource.readyState === EventSource.CONNECTING) {
        console.log("Attempting to reconnect...");
    }
};

import { onDestroy } from "../lib/skeleton/index.js";
import { loadCSS, loadJS } from "../helpers/loader.js";
import { getSession } from "../model/session.js";
import { all as allCommands, get as getCommand, register as registerCommand } from "./registry.js";

import("./commands.js");

export function init() {
    return;
}

class ComponentShell extends HTMLElement {
    constructor() {
        super();
        this.$handle = null;
        this.$term = null;
        this.term = null;
        this.home = null;
        this.cwd = "/";
        this.initialized = false;
        this.history = [];
        this.clear();
    }

    static get observedAttributes() {
        return ["class", "path"];
    }

    connectedCallback() {
        if (this.innerHTML === "") {
            this.innerHTML = `
                <div class="component_shell_handle"></div>
                <div class="component_shell_terminal"></div>
            `;
            this.$handle = this.querySelector(".component_shell_handle");
            this.$term = this.querySelector(".component_shell_terminal");
        }
        this.bootstrap();
    }

    attributeChangedCallback(name) {
        if (name === "path") {
            let path = this.getAttribute("path") || "/";
            if (!path.endsWith("/")) path = path.slice(0, path.lastIndexOf("/") + 1);
            this.cwd = path;
        }
        this.bootstrap();
    }

    async bootstrap() {
        if (this.classList.contains("hidden")) return;
        else if (this.initialized) {
            this.term.fit();
            this.term.clear();
            this.redrawLine();
            return;
        }
        this.initialized = true;
        await Promise.all([
            getSession().toPromise().then(({ home }) => {
                if (home === "/") return;
                this.home = home;
            }),
            loadCSS(import.meta.url, "./shell.css"),
            loadCSS(import.meta.url, "../lib/vendor/xterm/xterm.css"),
            loadJS(import.meta.url, "../lib/vendor/xterm/xterm.js"),
        ]);
        window.Terminal.applyAddon(window.fit);
        this.clear();
        this.term = new window.Terminal({
            cursorBlink: true,
            fontSize: 14,
            theme: {
                foreground: "#fafafa",
                background: "rgba(0, 0, 0, 0)",
                cursor: "#fafafa",
            },
            allowTransparency: true,
        });
        this.term.open(this.$term);
        this.term.write(this.getPrompt());
        this.term.fit();
        this.term.focus();
        this.term.on("key", (key, ev) => this.onKey(key, ev));
        this.initResize();
    }

    initResize() {
        let startY, startHeight;
        const onMouseMove = (ev) => {
            this.style.height = Math.min(
                window.innerHeight,
                Math.max(
                    100,
                    startHeight + (startY - ev.clientY),
                ),
            ) + "px";
            this.term.fit();
        };
        const onMouseUp = () => {
            document.body.classList.remove("shell_resizing");
            window.removeEventListener("mousemove", onMouseMove);
            window.removeEventListener("mouseup", onMouseUp);
        };
        this.$handle.onmousedown = (ev) => {
            startY = ev.clientY;
            startHeight = this.offsetHeight;
            document.body.classList.add("shell_resizing");
            window.addEventListener("mousemove", onMouseMove);
            window.addEventListener("mouseup", onMouseUp);
        };
    }

    onKey(key, ev) {
        if (ev.ctrlKey && ev.key === "c") {
            this.term.writeln("^C");
            if (this.running) {
                try { this.running(); } catch (err) { /* ignore */ }
                this.running = null;
                return;
            }
            this.prompt();
            return;
        } else if (this.running) {
            return;
        }

        if (ev.altKey && ev.key === "b") {
            let i = this.cursor - 1;
            while (i > 0 && this.line[i - 1] === " ") i--;
            while (i > 0 && this.line[i - 1] !== " ") i--;
            this.moveCursor(i - this.cursor);
            return;
        } else if (ev.altKey && ev.key === "f") {
            let i = this.cursor;
            while (i < this.line.length && this.line[i] === " ") i++;
            while (i < this.line.length && this.line[i] !== " ") i++;
            this.moveCursor(i - this.cursor);
            return;
        } else if (ev.ctrlKey && ev.key === "a") {
            this.moveCursor(-this.cursor);
            return;
        } else if (ev.ctrlKey && ev.key === "e") {
            this.moveCursor(this.line.length - this.cursor);
            return;
        } else if (ev.ctrlKey && ev.key === "k") {
            this.line = this.line.slice(0, this.cursor);
            this.redrawLine();
            return;
        } else if (ev.ctrlKey && ev.key === "l") {
            this.term.clear();
            this.redrawLine();
            return;
        } else if (ev.key === "Enter") {
            this.term.write("\r\n");
            const input = this.line.trim();
            if (input !== "") {
                this.history.push(input);
                const [id, ...args] = input.split(/\s+/);
                const command = getCommand(id);
                if (command) {
                    this.running = command.run(this, args);
                    if (!this.running) this.prompt();
                    return;
                }
                this.term.writeln(`command not found: ${id}`);
            }
            this.prompt();
            return;
        } else if (ev.key === "Backspace") {
            this.deleteAtCursor(-1);
            return;
        } else if (ev.key === "Delete") {
            this.deleteAtCursor(0);
            return;
        } else if (ev.key === "ArrowLeft") {
            this.moveCursor(-1);
            return;
        } else if (ev.key === "ArrowRight") {
            this.moveCursor(1);
            return;
        } else if (ev.key === "Home") {
            this.moveCursor(-this.cursor);
            return;
        } else if (ev.key === "End") {
            this.moveCursor(this.line.length - this.cursor);
            return;
        } else if (ev.key === "ArrowUp") {
            this.navigateHistory(1);
            return;
        } else if (ev.key === "ArrowDown") {
            this.navigateHistory(-1);
            return;
        } else if (ev.key === "Tab") {
            if (ev.preventDefault) ev.preventDefault();
            const matches = allCommands().map((c) => c.id).filter((id) => id.startsWith(this.line));
            if (matches.length === 1) this.replaceLine(matches[0]);
            else if (matches.length > 1) {
                this.term.write("\r\n");
                this.term.writeln(matches.join("  "));
                this.redrawLine();
            }
            return;
        }

        if (ev.altKey || ev.metaKey || ev.ctrlKey || key.length !== 1) return;
        this.line = this.line.slice(0, this.cursor) + key + this.line.slice(this.cursor);
        this.cursor += key.length;
        this.redrawLine();
    }

    getPrompt() {
        let path = this.cwd;
        if (this.home && path.startsWith(this.home)) {
            path = "~/" + path.slice(this.home.length);
        }
        return path + " # ";
    }

    prompt() {
        this.running = null;
        this.clear();
        this.term.write(this.getPrompt());
        this.term.fit();
    }

    redrawLine() {
        const prompt = this.getPrompt();
        this.term.write("\x1b[2K\r");
        this.term.write(prompt + this.line);
        const offset = this.line.length - this.cursor;
        if (offset > 0) this.term.write(`\x1b[${offset}D`);
    }

    replaceLine(line) {
        this.line = line;
        this.cursor = line.length;
        this.redrawLine();
    }

    clear() {
        this.line = "";
        this.cursor = 0;
        this.historyIndex = -1;
    }

    moveCursor(delta) {
        this.cursor = Math.max(0, Math.min(this.line.length, this.cursor + delta));
        this.redrawLine();
    }

    deleteAtCursor(offset) {
        const index = this.cursor + offset;
        if (index < 0 || index >= this.line.length) return;
        this.line = this.line.slice(0, index) + this.line.slice(index + 1);
        if (offset < 0) this.cursor -= 1;
        this.redrawLine();
    }

    navigateHistory(delta) {
        if (this.history.length === 0) return;
        this.historyIndex = Math.max(-1, Math.min(this.history.length - 1, this.historyIndex + delta));
        this.replaceLine(this.historyIndex === -1 ? "" : this.history[this.history.length - 1 - this.historyIndex]);
    }
}

customElements.define("component-shell", ComponentShell);

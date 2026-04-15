import { register, all } from "./registry.js";
import { join } from "../lib/path.js";
import rxjs from "../lib/rx.js";
import ajax from "../lib/ajax.js";
import { stat, cat } from "../pages/viewerpage/model_files.js";
import { getMimeType } from "../pages/viewerpage/mimetype.js";
import { get as getConfig } from "../model/config.js";
import { ls, touch, mkdir, rm, mv } from "../pages/filespage/model_files.js";

register({
    id: "help",
    description: "Show available commands",
    run(shell) {
        const commands = all();
        shell.term.writeln("Available commands:");
        const width = Math.max(...commands.map((c) => c.id.length));
        for (const cmd of commands) {
            shell.term.writeln(`  ${cmd.id.padEnd(width + 2)}${cmd.description}`);
        }
    },
});

register({
    id: "about",
    description: "Show information about your instance",
    run(shell) {
        const controller = new AbortController();
        fetch("/about", { signal: controller.signal })
            .then((res) => res.text())
            .then((html) => {
                const $doc = new DOMParser().parseFromString(html, "text/html");
                $doc.querySelectorAll("style, script").forEach((el) => el.remove());
                $doc.body.textContent
                    .split("\n")
                    .map((line) => line.trim())
                    .filter((line) => line !== "")
                    .forEach((line) => shell.term.writeln(line));
            })
            .catch((err) => {
                if (err.name === "AbortError") return;
                shell.term.writeln(`about: ${err.message}`);
            })
            .finally(() => shell.prompt());
        return () => controller.abort();
    },
});

register({
    id: "clear",
    description: "Clear the terminal screen",
    run(shell) {
        shell.term.clear();
    },
});

register({
    id: "exit",
    description: "Close the shell",
    run(shell) {
        shell.classList.add("hidden");
    },
});

register({
    id: "mimetype",
    description: "Determine file type",
    complete: complete(() => true),
    run(shell, args) {
        if (!args[0]) {
            shell.term.writeln("mimetype: missing operand");
            return;
        }
        const path = join(window.location.origin + shell.cwd, args[0]);
        const sub = stat("api/files/cat?path=" + encodeURIComponent(path)).pipe(
            rxjs.catchError((err) => {
                shell.term.writeln(`mimetype: ${err.message}`);
                return rxjs.EMPTY;
            }),
        ).subscribe({
            next({ type }) {
                shell.term.writeln(
                    type === "directory"
                        ? "inode/directory"
                        : getMimeType(args[0], getConfig("mime", {})),
                );
            },
            complete() { shell.prompt(); },
        });
        return () => sub.unsubscribe();
    },
});

// register({
//     id: "xdg-open",
//     description: "Open a file",
//     run(shell) {
//         // TODO
//     },
// });

// register({
//     id: "whoami",
//     description: "Print effective user name",
//     run(shell) {
//         // TODO
//     },
// });

// register({
//     id: "mkdir",
//     description: "Make directories",
//     run(shell) {
//         // TODO
//     },
// });

// register({
//     id: "rm",
//     description: "Remove files or directories",
//     run(shell) {
//         // TODO
//     },
// });

// register({
//     id: "mv",
//     description: "Move files",
//     run(shell) {
//         // TODO
//     },
// });

// register({
//     id: "touch",
//     description: "Create a file",
//     run(shell) {
//         // TODO
//     },
// });

// register({
//     id: "save",
//     description: "Save a file",
//     run(shell) {
//         // TODO
//     },
// });

register({
    id: "pwd",
    description: "Print current/working directory",
    run(shell) {
        shell.term.writeln(shell.cwd);
    },
});

register({
    id: "cd",
    description: "Change working directory",
    complete: complete(({ type }) => type === "directory"),
    run(shell, { args }) {
        if (!args[0]) {
            shell.cwd = shell.home || "/";
            return;
        }
        const path = join(window.location.origin + shell.cwd, args[0]).replace(new RegExp("\/?$"), "/");
        const sub = stat("api/files/cat?path=" + encodeURIComponent(path)).subscribe({
            next({ type }) {
                if (type !== "directory") shell.term.writeln("cd: not a directory");
                else shell.cwd = path;
            },
            error(err) { shell.term.writeln(`cd: ${err.message}`); },
            complete() { shell.prompt(); },
        });
        return () => sub.unsubscribe();
    },
});

register({
    id: "ls",
    description: "List directory contents",
    complete: complete(({ type }) => type === "directory"),
    run(shell, { args, hasOption }) {
        const opts = {
            "all": hasOption("a"),
            "long": hasOption("l"),
            "human": hasOption("h"),
        };
        const path = args[0] ?
              join(window.location.origin + shell.cwd, args[0]).replace(new RegExp("\/?$"), "/")
              : shell.cwd;

        const sub = ls(path).pipe(
            rxjs.debounceTime(100),
            rxjs.first(),
            rxjs.catchError((err) => {
                shell.term.writeln(`ls: ${err.message}`);
                return rxjs.EMPTY;
            }),
        ).subscribe({
            next({ files, permissions }) {
                const output = files
                      .filter(({ name }) => name.startsWith(".") ? opts["all"] : true)
                      .map(({ type, name, size, time, offline }) => {
                          const isDir = type === "directory";
                          if (!opts["long"]) return isDir ? `\x1b[1;34m${name}\x1b[0m` : name;
                          const perm = (function () {
                              let out = "";
                              out += (isDir ? "d" : "-");
                              out += (isDir ? "r" : (offline ? "-" : "r"));
                              out += (isDir && (permissions["can_create_directory"] !== false)) ? "w" :
                                  (!isDir && (permissions["can_create_file"] !== false)) ? "w" :
                                  "-";
                              out += (isDir ? "x" : "-");
                              return out;
                          }());
                          const n = isDir ? `\x1b[1;34m${name}\x1b[0m` : name;
                          const s = isDir ? "-" : opts["human"] ? (function (bytes) {
                              if (bytes < 1024) return bytes + "B";
                              if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + "K";
                              if (bytes < 1024 * 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(1) + "M";
                              return (bytes / (1024 * 1024 * 1024)).toFixed(1) + "G";
                          }(size)) : `${size}`;
                          const d = (function (t) {
                              if (t === 0) return "";
                              const dt = new Date(t);
                              const month = dt.toLocaleDateString("en-US", { month: "short" });
                              const day = String(dt.getDate()).padStart(2);
                              const hr = dt.toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit" });
                              return `${month} ${day} ${hr}`;
                          })(time);
                          return `${perm}  ${s.padStart(8)}  ${d}  ${n}`;
                      })
                      .join(opts["long"] ? "\r\n" : "  ");
                shell.term.writeln(output);
            },
            complete() { shell.prompt(); },
        });
        return () => sub.unsubscribe();
    },
});

register({
    id: "stat",
    description: "Display file info",
    complete: complete(() => true),
    run(shell, { args }) {
        if (!args[0]) {
            shell.term.writeln("stat: missing operand");
            return;
        }
        const path = join(window.location.origin + shell.cwd, args[0]);
        const sub = stat("api/files/cat?path=" + encodeURIComponent(path)).subscribe({
            next({ type, size, time }) {
                shell.term.writeln(`  File: ${args[0]}`);
                shell.term.writeln(`  Type: ${type}`);
                shell.term.writeln(`  Size: ${size}`);
                shell.term.writeln(`Modify: ${new Date(time).toLocaleString()}`);
            },
            error(err) { shell.term.writeln(`stat: ${err.message}`); },
            complete() { shell.prompt(); },
        });
        return () => sub.unsubscribe();
    },
});

register({
    id: "cat",
    description: "Display file contents",
    complete: complete(() => true),
    run(shell, { args }) {
        if (!args[0]) {
            shell.term.writeln("cat: missing operand");
            return;
        }
        const path = join(window.location.origin + shell.cwd, args[0]);
        const sub = cat("api/files/cat?path=" + encodeURIComponent(path)).pipe(
            rxjs.catchError((err) => {
                shell.term.writeln(`cat: ${err.message}`);
                return rxjs.EMPTY;
            }),
        ).subscribe({
            next(response) {
                const text = typeof response === "string"
                    ? response
                    : new TextDecoder().decode(response);
                text.split("\n").forEach((line) => shell.term.writeln(line));
            },
            complete() { shell.prompt(); },
        });
        return () => sub.unsubscribe();
    },
});

function complete(filter) {
    return function (shell, partial) {
        const slashIdx = partial.lastIndexOf("/") + 1;
        const [dir, prefix] = [
            partial.slice(0, slashIdx),
            partial.slice(slashIdx),
        ];
        ls(join(window.location.origin + shell.cwd, dir)).pipe(
            rxjs.debounceTime(100),
            rxjs.first(),
            rxjs.catchError(() => rxjs.EMPTY),
            rxjs.map(({ files }) => files
                .filter((f) => filter(f) && f.name.startsWith(prefix))
                .map((f) => f.name + (f.type === "directory" ? "/" : ""))),
        ).subscribe((matches) => {
            switch (matches.length) {
            case 0:
                return;
            case 1:
                const parts = shell.line.split(/\s+/);
                parts[parts.length - 1] = dir + matches[0];
                shell.replaceLine(parts.join(" "));
                return;
            default:
                shell.term.write("\r\n");
                shell.term.writeln(matches.join("  "));
                shell.redrawLine();
                return;
            }
        });
    };
}

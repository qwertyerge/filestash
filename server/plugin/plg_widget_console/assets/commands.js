import { register, all } from "./registry.js";

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
    id: "exit",
    description: "Close the shell",
    run(shell) {
        shell.classList.add("hidden");
    },
});

// register({
//     id: "mimetype",
//     description: "Determine file type",
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
//     id: "pwd",
//     description: "Print current/working directory",
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

// register({
//     id: "cd",
//     description: "Change working directory",
//     run(shell) {
//         // TODO
//     },
// });

// register({
//     id: "ls",
//     description: "List directory contents",
//     run(shell) {
//         // TODO
//     },
// });

// register({
//     id: "stat",
//     description: "Display file info",
//     run(shell) {
//         // TODO
//     },
// });

// register({
//     id: "xdg-open",
//     description: "Open a file",
//     run(shell) {
//         // TODO
//     },
// });

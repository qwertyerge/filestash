import rxjs, { effect } from "../../lib/rx.js";

import { extractPath, isDir, currentPath } from "./helper.js";
import { mv as mv$, refresh } from "./model_files.js";
import { mv as mvVL, withVirtualLayer } from "./model_virtual_layer.js";

const mv = (from, to) => withVirtualLayer(
    mv$(from, to),
    mvVL(from, to),
);

export default function() {
    effect(rxjs.fromEvent(document.body, "dragover").pipe(
        rxjs.withLatestFrom(rxjs.merge(
            rxjs.fromEvent(document, "dragstart").pipe(rxjs.map(() => true)),
            rxjs.fromEvent(document, "dragend").pipe(rxjs.map(() => false)),
            rxjs.of(false),
        )),
        rxjs.filter(([, local]) => !local),
        rxjs.tap(([e]) => {
            e.preventDefault();
            e.dataTransfer.dropEffect = "move";
        }),
    ));

    effect(rxjs.fromEvent(document.body, "drop").pipe(
        rxjs.withLatestFrom(rxjs.merge(
            rxjs.fromEvent(document, "dragstart").pipe(rxjs.map(() => true)),
            rxjs.fromEvent(document, "dragend").pipe(rxjs.map(() => false)),
            rxjs.of(false),
        )),
        rxjs.filter(([, local]) => !local),
        rxjs.tap(([e]) => e.preventDefault()),
        rxjs.map(([e]) => {
            const from = e.dataTransfer.getData("path");
            const [, name] = extractPath(from);
            let to = currentPath() + name;
            if (isDir(from)) to += "/";
            return { e, from, to };
        }),
        rxjs.filter(({ from, to }) => from !== "" && from !== to),
        rxjs.mergeMap(({ from, to }) => mv(from, to)),
        rxjs.tap(refresh),
    ));
}

<?php
require_once __DIR__ . '/lib/worker_base.php';

$opts = eregion_parse_args($argv);
eregion_serve($opts, function (array $req) {
    $path = $req['path'] ?? '/';
    if ($path === '/crash') {
        fwrite(STDERR, "crashing on purpose\n");
        exit(1);
    }
    return eregion_ok_response($req, 'before-crash');
});

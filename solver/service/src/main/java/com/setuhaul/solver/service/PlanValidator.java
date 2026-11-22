package com.setuhaul.solver.service;

import jakarta.annotation.PostConstruct;
import jakarta.enterprise.context.ApplicationScoped;

import java.io.IOException;
import java.io.InputStream;
import java.net.URI;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.nio.file.FileSystem;
import java.nio.file.FileSystemNotFoundException;
import java.nio.file.FileSystems;
import java.nio.file.Files;
import java.nio.file.Path;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import java.util.stream.Stream;

/**
 * Runs the plan checks in db/validate (V1 to V8) against a stored run. They are plain SQL that shares no code with
 * the solver, the same files the Python validator runs; the build copies them onto the classpath under validate/.
 */
@ApplicationScoped
public class PlanValidator {

    private static final Pattern HEADER = Pattern.compile("^--\\s*(id|title):\\s*(.+?)\\s*$", Pattern.MULTILINE);
    private static final String RUN_ID_PARAM = "%(run_id)s";

    record Check(String id, String title, String sql, int params) {
    }

    List<Check> checks;

    @PostConstruct
    void init() throws IOException {
        checks = load();
        if (checks.isEmpty()) {
            throw new IllegalStateException("no plan checks found on the classpath under validate/");
        }
    }

    /** All violations of the run; empty means valid. Runs read-only in one transaction. */
    public List<Api.Violation> validate(Connection c, String runId) throws SQLException {
        boolean autoCommit = c.getAutoCommit();
        c.setAutoCommit(false);
        List<Api.Violation> found = new ArrayList<>();
        try {
            try (Statement st = c.createStatement()) {
                st.execute("SET TRANSACTION READ ONLY");
            }
            for (Check check : checks) {
                try (PreparedStatement ps = c.prepareStatement(check.sql())) {
                    for (int i = 1; i <= check.params(); i++) {
                        ps.setString(i, runId);
                    }
                    try (ResultSet rs = ps.executeQuery()) {
                        while (rs.next()) {
                            found.add(new Api.Violation(check.id(), check.title(), rs.getString(1), rs.getString(2)));
                        }
                    }
                }
            }
            c.commit();
        } catch (SQLException | RuntimeException e) {
            c.rollback();
            throw e;
        } finally {
            c.setAutoCommit(autoCommit);
        }
        return found;
    }

    List<String> checkIds() {
        return checks.stream().map(Check::id).toList();
    }

    private static List<Check> load() throws IOException {
        URL dir = PlanValidator.class.getClassLoader().getResource("validate");
        if (dir == null) {
            return List.of();
        }
        try {
            URI uri = dir.toURI();
            if ("jar".equals(uri.getScheme())) {
                FileSystem fs;
                try {
                    fs = FileSystems.getFileSystem(uri);
                } catch (FileSystemNotFoundException e) {
                    fs = FileSystems.newFileSystem(uri, Map.of());
                }
                return read(fs.provider().getPath(uri));
            }
            return read(Path.of(uri));
        } catch (java.net.URISyntaxException e) {
            throw new IOException(e);
        }
    }

    private static List<Check> read(Path dir) throws IOException {
        List<Check> out = new ArrayList<>();
        try (Stream<Path> files = Files.list(dir)) {
            for (Path p : files.filter(f -> f.getFileName().toString().endsWith(".sql")).sorted().toList()) {
                String sql;
                try (InputStream in = Files.newInputStream(p)) {
                    sql = new String(in.readAllBytes(), StandardCharsets.UTF_8);
                }
                String id = null;
                String title = null;
                Matcher m = HEADER.matcher(sql);
                while (m.find()) {
                    if (m.group(1).equals("id")) {
                        id = m.group(2);
                    } else {
                        title = m.group(2);
                    }
                }
                if (id == null || title == null) {
                    throw new IllegalStateException(p.getFileName() + ": needs '-- id:' and '-- title:' header lines");
                }
                int params = 0;
                for (int i = sql.indexOf(RUN_ID_PARAM); i >= 0; i = sql.indexOf(RUN_ID_PARAM, i + 1)) {
                    params++;
                }
                // Strip the trailing semicolon: one statement per prepared query.
                String jdbc = sql.replace(RUN_ID_PARAM, "?").strip();
                if (jdbc.endsWith(";")) {
                    jdbc = jdbc.substring(0, jdbc.length() - 1);
                }
                out.add(new Check(id, title, jdbc, params));
            }
        }
        return out;
    }
}

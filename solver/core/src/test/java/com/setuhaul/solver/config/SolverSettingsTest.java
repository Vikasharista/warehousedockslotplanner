package com.setuhaul.solver.config;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

class SolverSettingsTest {

    @Test
    void defaultsMatchThePlan() {
        SolverSettings s = SolverSettings.defaults();
        assertThat(s.gateBufferMin()).isEqualTo(30);
        assertThat(s.lowConfidenceMarginMin()).isEqualTo(15);
        assertThat(s.holdExpiryMin()).isEqualTo(15);
        assertThat(s.horizonHours()).isEqualTo(12);
        assertThat(s.priorityWeight("LOW")).isEqualTo(1);
        assertThat(s.priorityWeight("NORMAL")).isEqualTo(3);
        assertThat(s.priorityWeight("HIGH")).isEqualTo(9);
        assertThat(s.priorityWeight("CRITICAL")).isEqualTo(27);
        assertThat(s.unforcedMovePenalty()).isEqualTo(240);
        assertThat(s.forcedMovePenalty()).isEqualTo(30);
        assertThat(s.pinFeasibleConfirmed()).isTrue();
    }

    @Test
    void anOverrideFileReplacesOnlyTheKeysItNames(@TempDir Path dir) throws Exception {
        Path f = dir.resolve("site.properties");
        Files.writeString(f, "gate_buffer_min=20\npriority_weight.CRITICAL=50\n");
        SolverSettings s = SolverSettings.withOverrides(f);
        assertThat(s.gateBufferMin()).isEqualTo(20);
        assertThat(s.priorityWeight("CRITICAL")).isEqualTo(50);
        assertThat(s.holdExpiryMin()).isEqualTo(15);
    }

    @Test
    void aMistypedKeyIsRejectedRatherThanIgnored(@TempDir Path dir) throws Exception {
        Path f = dir.resolve("typo.properties");
        Files.writeString(f, "gate_bufer_min=20\n");
        assertThatThrownBy(() -> SolverSettings.withOverrides(f)).hasMessageContaining("unknown setting 'gate_bufer_min'");
    }

    @Test
    void negativeBuffersAreRejected(@TempDir Path dir) throws Exception {
        Path f = dir.resolve("bad.properties");
        Files.writeString(f, "gate_buffer_min=-5\n");
        assertThatThrownBy(() -> SolverSettings.withOverrides(f)).isInstanceOf(IllegalArgumentException.class);
    }

    @org.junit.jupiter.api.Test
    void bumpingDefaultsAndTheirSafetyRule() throws Exception {
        var b = SolverSettings.defaults().bumping();
        org.assertj.core.api.Assertions.assertThat(b.enabled()).isTrue();
        org.assertj.core.api.Assertions.assertThat(b.by()).containsExactly("CRITICAL");
        org.assertj.core.api.Assertions.assertThat(b.victims()).containsExactlyInAnyOrder("LOW", "NORMAL");
        org.assertj.core.api.Assertions.assertThat(b.freezeMin()).isEqualTo(120);
        // A penalty below HIGH's weight (9) would let HIGH trucks justify bumps; at or above CRITICAL's (27) nothing could.
        for (String penalty : new String[] {"5", "27", "40"}) {
            var f = java.nio.file.Files.createTempFile("bump", ".properties");
            java.nio.file.Files.writeString(f, "bump_penalty=" + penalty + "\n");
            org.assertj.core.api.Assertions.assertThatThrownBy(() -> SolverSettings.withOverrides(f))
                    .isInstanceOf(IllegalArgumentException.class).hasMessageContaining("bump_penalty");
        }
        var both = java.nio.file.Files.createTempFile("bump", ".properties");
        java.nio.file.Files.writeString(both, "bump_victims=LOW,CRITICAL\n");
        org.assertj.core.api.Assertions.assertThatThrownBy(() -> SolverSettings.withOverrides(both))
                .hasMessageContaining("cannot both bump and be bumped");
        var off = java.nio.file.Files.createTempFile("bump", ".properties");
        java.nio.file.Files.writeString(off, "bump_enabled=false\nbump_penalty=0\n");
        org.assertj.core.api.Assertions.assertThat(SolverSettings.withOverrides(off).bumping().enabled()).isFalse();
    }
}

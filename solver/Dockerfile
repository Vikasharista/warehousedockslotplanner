# Solver service. Build from the repository root: docker build -f solver/Dockerfile .
# The first build downloads the Maven dependencies (a few hundred MB, several minutes); the cache mount keeps
# them for later builds.
FROM maven:3.9.11-eclipse-temurin-21 AS build
WORKDIR /src
COPY solver/pom.xml solver/pom.xml
COPY solver/core/pom.xml solver/core/pom.xml
COPY solver/service/pom.xml solver/service/pom.xml
COPY solver/core/src solver/core/src
COPY solver/service/src solver/service/src
# The plan checks the commit path runs, shared with the Python validator.
COPY db/validate db/validate
RUN --mount=type=cache,target=/root/.m2 mvn -B -f solver/pom.xml -DskipTests package

FROM eclipse-temurin:21-jre
WORKDIR /app
COPY --from=build /src/solver/service/target/quarkus-app /app
EXPOSE 8081
CMD ["java", "-jar", "/app/quarkus-run.jar"]

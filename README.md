# Экзамен по дисциплине "Основы DevOps" - развертывание Noters в K8s

Noters — мини-сервис личных заметок: собственный блокнот для каждой учётной записи, интерфейс на русском языке и данные в PostgreSQL. Можно создавать, редактировать, удалять и закреплять заметки, искать по заголовку и тексту. Закреплённые заметки идут первыми, остальные — по времени последнего изменения. Интерфейс адаптирован для телефона и компьютера.

## Задание
1) Изучить проект, развертывание, сборку. Можешь даже его локально запустить
2) Собрать его и пушнуть в свой `hub.docker.com` (или любой другой публичный registry для контейнеров на твое усмотрение) контейнеры backend-а и frontend-а
3) Написать манифесты для развертывания: Deployment, Service, Cluster (для cloudnative-pg), HTTPRoute (используется gateway-api)


## Полезная информация
1) Кубоконфиг скачать тут - (Предварительно регистрируемся)
2) StorageClass - `cloudru-ssd`
3) Общий URL - k8s.public.letoctf.ru, ваш сервис должен быть доступен по k8s.public.letoctf.ru/stud-XX (номер будет совпадать с неймспейсом кубоконфига)
4) Образы лучше заливать через mirror.gcr.io (например, mirror.gcr.io/nginx:latest)
5) TLS уже терминируется, для него ничего делать не надо

## Рекомендуемые парамтеры
```yaml
backend:
  image:
    pullPolicy: IfNotPresent
  replicas: 1
  resources:
    requests: {cpu: 100m, memory: 128Mi}
    limits: {cpu: 500m, memory: 256Mi}

frontend:
  image:
    pullPolicy: IfNotPresent
  replicas: 1
  resources:
    requests: {cpu: 25m, memory: 64Mi}
    limits: {cpu: 250m, memory: 128Mi}

route:
  hostname: k8s.public.letoctf.ru
  # Вместо XX - ваш номер. Ну или вообще можете оставить пустым, оно возьмет название NS
  basePath: "/stud-XX"
  gateway:
    name: public
    namespace: envoy-gateway-system
    sectionName: https

database:
  image: k8s.public.letoctf.ru/cloudnative-pg/postgresql:17.9
  instances: 1
  name: noters
  owner: noters
  storage:
    size: 2Gi
    storageClass: cloudru-ssd
  resources:
    requests: {cpu: 100m, memory: 256Mi}
    limits: {cpu: 500m, memory: 512Mi}
```

## Результат - ваше приложение должно быть развернуто 

Пример - [https://k8s.public.letoctf.ru/stud-61/](https://k8s.public.letoctf.ru/stud-61/)
У вас будет такой же урл, только номер в конце будет отличаться